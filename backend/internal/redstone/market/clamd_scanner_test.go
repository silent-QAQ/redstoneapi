package market

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClamdDeliveryScannerStreamsPayloadAndMapsVerdicts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	received := make(chan []byte, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		command := make([]byte, len("zINSTREAM\x00"))
		_, _ = io.ReadFull(conn, command)
		var body []byte
		for {
			var size [4]byte
			_, _ = io.ReadFull(conn, size[:])
			length := binary.BigEndian.Uint32(size[:])
			if length == 0 {
				break
			}
			chunk := make([]byte, length)
			_, _ = io.ReadFull(conn, chunk)
			body = append(body, chunk...)
		}
		received <- body
		_, _ = io.WriteString(conn, "stream: OK\x00")
	}()

	scanner, err := NewClamdDeliveryScanner(listener.Addr().String())
	require.NoError(t, err)
	result, err := scanner.Scan(context.Background(), DeliveryScanInput{Content: []byte("secret-upload-content")})
	require.NoError(t, err)
	require.Equal(t, SellerScanPassed, result)
	require.Equal(t, []byte("secret-upload-content"), <-received)
}

func TestClamdDeliveryScannerMapsFoundWithoutLeakingSignature(t *testing.T) {
	result, err := parseClamdVerdict("stream: Eicar-Test-Signature FOUND")
	require.NoError(t, err)
	require.Equal(t, SellerScanRejected, result)
}
