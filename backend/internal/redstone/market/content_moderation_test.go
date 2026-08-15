package market

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestDeterministicContentModerationRejectsPublicCredentialWithoutRetainingPlaintext(t *testing.T) {
	scanner := NewDeterministicContentModerationScanner()
	secret := "sk-super-secret-value-that-must-not-be-persisted"
	decision, err := scanner.Scan(context.Background(), ContentModerationInput{
		Scope: ContentScopeProductMetadata, ProductType: "text_key", Title: "Credential", Description: secret,
	})
	require.NoError(t, err)
	require.Equal(t, ContentModerationRejected, decision.Verdict)
	require.Equal(t, []string{"credential_material_detected"}, decision.FindingCodes)
	require.Len(t, decision.ContentSHA256, 64)
	serialized, err := json.Marshal(decision)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), secret)
}

func TestDeterministicContentModerationRoutesDeliveryCredentialToManualReview(t *testing.T) {
	decision, err := NewDeterministicContentModerationScanner().Scan(context.Background(), ContentModerationInput{
		Scope: ContentScopeDeliveryContent, ProductType: "text_key", Content: []byte("access_token=very-secret-token-value-12345"),
	})
	require.NoError(t, err)
	require.Equal(t, ContentModerationManualReview, decision.Verdict)
	require.Equal(t, []string{"credential_material_detected"}, decision.FindingCodes)
}

func TestDeterministicContentModerationRoutesAccountReferenceToManualReview(t *testing.T) {
	decision, err := NewDeterministicContentModerationScanner().Scan(context.Background(), ContentModerationInput{
		Scope: ContentScopeAccountReference, ProductType: "account_reference", Title: "Gemini account",
	})
	require.NoError(t, err)
	require.Equal(t, ContentModerationManualReview, decision.Verdict)
	require.Equal(t, []string{"account_transfer_requires_review"}, decision.FindingCodes)
}

func TestDeterministicContentModerationRejectsHighRiskTransactionIndicators(t *testing.T) {
	decision, err := NewDeterministicContentModerationScanner().Scan(context.Background(), ContentModerationInput{
		Scope: ContentScopeProductMetadata, ProductType: "text_key", Title: "绕过验证服务",
	})
	require.NoError(t, err)
	require.Equal(t, ContentModerationRejected, decision.Verdict)
	require.Equal(t, []string{"high_risk_transaction_indicator"}, decision.FindingCodes)
}

func TestDeterministicContentModerationSkipsOpaqueBinaryTextRules(t *testing.T) {
	decision, err := NewDeterministicContentModerationScanner().Scan(context.Background(), ContentModerationInput{
		Scope: ContentScopeDeliveryContent, ProductType: "file", Content: []byte{0xff, 0xfe, 0x00, 0x01},
	})
	require.NoError(t, err)
	require.Equal(t, ContentModerationPassed, decision.Verdict)
}

func TestContentModerationUnavailableIsFailClosed(t *testing.T) {
	service, err := NewService(&sellerRepositoryStub{})
	require.NoError(t, err)
	service.SetContentModerationScanner(nil)
	_, err = service.scanProductMetadata(context.Background(), "text_key", "ordinary title", "ordinary description")
	require.ErrorIs(t, err, ErrContentModerationUnavailable)
	require.Equal(t, int32(503), infraerrors.FromError(marketApplicationError(err)).Code)
}

func TestResolveContentReviewRequestValidation(t *testing.T) {
	valid := ResolveContentReviewRequest{ActorUserID: 1, ReviewID: 2, Action: contentReviewActionApprove, Note: "Reviewed and approved"}
	require.NoError(t, valid.Validate())
	require.ErrorIs(t, (ResolveContentReviewRequest{ActorUserID: 1, ReviewID: 2, Action: "ignore", Note: "note"}).Validate(), ErrInvalidContentReviewAction)
	require.ErrorIs(t, (ResolveContentReviewRequest{ActorUserID: 1, ReviewID: 2, Action: contentReviewActionReject}).Validate(), ErrReportReasonRequired)
}

func TestContentModerationErrorsHaveNonSensitivePublicMapping(t *testing.T) {
	err := marketApplicationError(ErrContentModerationRejected)
	require.Equal(t, int32(409), infraerrors.FromError(err).Code)
	require.NotContains(t, err.Error(), "credential")
	require.False(t, errors.Is(err, ErrContentModerationUnavailable))
}
