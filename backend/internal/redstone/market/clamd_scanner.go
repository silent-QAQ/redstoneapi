package market

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/silent-QAQ/redstoneapi/internal/config"
)

const clamdChunkBytes = 64 << 10

var ErrClamdAddressRequired = errors.New("market clamd address is required")

// ClamdDeliveryScanner uses ClamAV's zINSTREAM protocol. It never includes
// upload bytes or malware signature names in returned errors.
type ClamdDeliveryScanner struct {
	address string
	dial    net.Dialer
}

func NewClamdDeliveryScanner(address string) (*ClamdDeliveryScanner, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, ErrClamdAddressRequired
	}
	return &ClamdDeliveryScanner{address: address}, nil
}

// ProvideDeliveryScanner leaves uploads fail-closed when ClamAV is not
// configured. Returning nil here is intentional and is handled by Service.
func ProvideDeliveryScanner(cfg *config.Config) (DeliveryScanner, error) {
	if cfg == nil {
		return nil, nil
	}
	if !cfg.MarketplaceScanner.Active() {
		if PreviewInfrastructureEnabled() {
			return previewDeliveryScanner{}, nil
		}
		return nil, nil
	}
	return NewClamdDeliveryScanner(cfg.MarketplaceScanner.ClamdAddress)
}

func (s *ClamdDeliveryScanner) Scan(ctx context.Context, input DeliveryScanInput) (SellerScanResult, error) {
	if s == nil || s.address == "" {
		return "", ErrClamdAddressRequired
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return "", fmt.Errorf("connect marketplace scanner: %w", err)
	}
	defer conn.Close()
	if err := writeClamdStream(conn, input.Content); err != nil {
		return "", err
	}
	response, err := bufio.NewReader(conn).ReadString(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read marketplace scanner result: %w", err)
	}
	return parseClamdVerdict(response)
}

func (s *ClamdDeliveryScanner) HealthCheck(ctx context.Context) error {
	if s == nil || s.address == "" {
		return ErrClamdAddressRequired
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return fmt.Errorf("connect marketplace scanner: %w", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "zPING\x00"); err != nil {
		return fmt.Errorf("ping marketplace scanner: %w", err)
	}
	response, err := bufio.NewReader(conn).ReadString(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read marketplace scanner health: %w", err)
	}
	if strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(response, "\x00"))) != "PONG" {
		return errors.New("marketplace scanner health response is invalid")
	}
	return nil
}

func (s *ClamdDeliveryScanner) connect(ctx context.Context) (net.Conn, error) {
	conn, err := s.dial.DialContext(ctx, "tcp", s.address)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func parseClamdVerdict(response string) (SellerScanResult, error) {
	response = strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(response, "\x00")))
	switch {
	case strings.HasSuffix(response, "OK"):
		return SellerScanPassed, nil
	case strings.Contains(response, "FOUND"):
		return SellerScanRejected, nil
	default:
		return "", errors.New("marketplace scanner returned an invalid result")
	}
}

func writeClamdStream(writer io.Writer, content []byte) error {
	if _, err := io.WriteString(writer, "zINSTREAM\x00"); err != nil {
		return fmt.Errorf("start marketplace scanner stream: %w", err)
	}
	for len(content) > 0 {
		chunk := content
		if len(chunk) > clamdChunkBytes {
			chunk = content[:clamdChunkBytes]
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(chunk)))
		if _, err := writer.Write(length[:]); err != nil {
			return fmt.Errorf("write marketplace scanner chunk length: %w", err)
		}
		if _, err := writer.Write(chunk); err != nil {
			return fmt.Errorf("write marketplace scanner chunk: %w", err)
		}
		content = content[len(chunk):]
	}
	_, err := writer.Write([]byte{0, 0, 0, 0})
	if err != nil {
		return fmt.Errorf("finish marketplace scanner stream: %w", err)
	}
	return nil
}
