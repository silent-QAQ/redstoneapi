package market

import (
	"bytes"
	"context"
	"os"
	"strings"
)

// PreviewInfrastructureEnv is deliberately separate from production config.
// The preview launcher opts into local ciphertext storage and the deterministic
// scanner explicitly; an unconfigured production process remains fail-closed.
const PreviewInfrastructureEnv = "REDSTONE_MARKETPLACE_PREVIEW"

func PreviewInfrastructureEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(PreviewInfrastructureEnv)), "1") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv(PreviewInfrastructureEnv)), "true")
}

// previewDeliveryScanner is only for the local preview. It catches the public
// EICAR test signature without sending plaintext to a third-party service.
// Production deployments must use ClamAV through ClamdDeliveryScanner.
type previewDeliveryScanner struct{}

func (previewDeliveryScanner) HealthCheck(context.Context) error { return nil }

func (previewDeliveryScanner) Scan(_ context.Context, input DeliveryScanInput) (SellerScanResult, error) {
	if bytes.Contains(bytes.ToUpper(input.Content), []byte("X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*")) {
		return SellerScanRejected, nil
	}
	return SellerScanPassed, nil
}
