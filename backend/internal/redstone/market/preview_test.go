package market

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreviewDeliveryScannerRejectsEICARSignature(t *testing.T) {
	scanner := previewDeliveryScanner{}
	verdict, err := scanner.Scan(context.Background(), DeliveryScanInput{
		Content: []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`),
	})
	require.NoError(t, err)
	require.Equal(t, SellerScanRejected, verdict)

	verdict, err = scanner.Scan(context.Background(), DeliveryScanInput{Content: []byte("ordinary preview content")})
	require.NoError(t, err)
	require.Equal(t, SellerScanPassed, verdict)
}
