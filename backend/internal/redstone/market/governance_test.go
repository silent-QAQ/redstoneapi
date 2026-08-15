package market

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGovernanceRequestsRequireActorAndBoundedReason(t *testing.T) {
	report := CreateReportRequest{ReporterUserID: 7, ProductID: 8, Reason: "misleading listing"}
	require.NoError(t, report.Validate())
	require.ErrorIs(t, (CreateReportRequest{ReporterUserID: 7, ProductID: 8}).Validate(), ErrReportReasonRequired)

	resolution := ResolveReportRequest{ActorUserID: 9, ReportID: 2, Action: reportResolutionSuspend, Note: "policy violation"}
	require.NoError(t, resolution.Validate())
	resolution.Action = reportResolutionRelease
	require.NoError(t, resolution.Validate())
	resolution.Action = "unknown"
	require.ErrorIs(t, resolution.Validate(), ErrInvalidReportResolution)

	reversal := ReverseOrderRequest{ActorUserID: 9, OrderID: 12, Reason: "settlement correction"}
	require.NoError(t, reversal.Validate())
	require.ErrorIs(t, (ReverseOrderRequest{ActorUserID: 9, OrderID: 12}).Validate(), ErrReportReasonRequired)
}
