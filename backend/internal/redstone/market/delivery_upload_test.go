package market

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeliveryUploadSQLAllowsSoldOutProductsToReenterScan(t *testing.T) {
	// Keep this contract explicit: a sold-out non-account listing must be
	// restockable, but InsertSellerDeliveryItem always sets pending_scan before
	// it can become active again.
	require.True(t, deliveryUploadAllowedStatus("sold_out"))
	require.True(t, deliveryUploadAllowedStatus("active"))
	require.False(t, deliveryUploadAllowedStatus("suspended"))
	require.False(t, deliveryUploadAllowedStatus("archived"))
}
