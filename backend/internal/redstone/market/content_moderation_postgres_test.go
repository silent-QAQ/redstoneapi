package market

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestRecordContentReviewPersistsOnlyDecisionMetadata(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	decision := ContentModerationDecision{
		Verdict: ContentModerationRejected, FindingCodes: []string{"credential_material_detected"}, ContentSHA256: strings.Repeat("a", 64),
	}
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_market_content_reviews (")).
		WithArgs(int64(7), nil, nil, ContentScopeProductMetadata, ContentModerationRejected, "closed", `["credential_material_detected"]`, strings.Repeat("a", 64), "auto_rejected", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "seller_user_id", "product_id", "delivery_item_id", "scope", "verdict", "review_state", "finding_codes", "resolution", "resolved_by_user_id", "resolution_note", "created_at", "resolved_at",
		}).AddRow(31, 7, nil, nil, ContentScopeProductMetadata, ContentModerationRejected, "closed", []byte(`["credential_material_detected"]`), "auto_rejected", nil, "", now, now))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO redstone_market_governance_audit")).
		WithArgs("content_review", int64(31), "content_auto_rejected", int64(7), "credential_material_detected", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	review, err := repository.RecordContentReview(context.Background(), RecordContentReviewRequest{
		SellerUserID: 7, Scope: ContentScopeProductMetadata, Decision: decision,
	})
	require.NoError(t, err)
	require.Equal(t, int64(31), review.ID)
	require.Nil(t, review.ProductID)
	require.Equal(t, []string{"credential_material_detected"}, review.FindingCodes)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRecordContentReviewStoresNoFindingsAsJSONArray(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_market_content_reviews (")).
		WithArgs(int64(7), nil, nil, ContentScopeDeliveryContent, ContentModerationPassed, "closed", "[]", strings.Repeat("b", 64), "auto_passed", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "seller_user_id", "product_id", "delivery_item_id", "scope", "verdict", "review_state", "finding_codes", "resolution", "resolved_by_user_id", "resolution_note", "created_at", "resolved_at",
		}).AddRow(32, 7, nil, nil, ContentScopeDeliveryContent, ContentModerationPassed, "closed", []byte("[]"), "auto_passed", nil, "", now, now))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO redstone_market_governance_audit")).
		WithArgs("content_review", int64(32), "content_auto_passed", int64(7), "", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	review, err := repository.RecordContentReview(context.Background(), RecordContentReviewRequest{
		SellerUserID: 7, Scope: ContentScopeDeliveryContent,
		Decision: ContentModerationDecision{Verdict: ContentModerationPassed, ContentSHA256: strings.Repeat("b", 64)},
	})
	require.NoError(t, err)
	require.Empty(t, review.FindingCodes)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveContentReviewAuditsAdminDecision(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(contentReviewSelect + " WHERE id = $1 FOR UPDATE")).
		WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "seller_user_id", "product_id", "delivery_item_id", "scope", "verdict", "review_state", "finding_codes", "resolution", "resolved_by_user_id", "resolution_note", "created_at", "resolved_at",
		}).AddRow(41, 7, nil, nil, ContentScopeProductMetadata, ContentModerationManualReview, "open", []byte(`["credential_listing_requires_review"]`), "", nil, "", now, nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE redstone_market_content_reviews")).
		WithArgs(int64(41), "admin_approved", "Operator reviewed it", int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO redstone_market_governance_audit")).
		WithArgs("content_review", int64(41), "admin_approved", int64(9), "Operator reviewed it", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	review, err := repository.ResolveContentReview(context.Background(), ResolveContentReviewRequest{
		ActorUserID: 9, ReviewID: 41, Action: contentReviewActionApprove, Note: "Operator reviewed it",
	}, decimal.NewFromInt(1))
	require.NoError(t, err)
	require.Equal(t, "resolved", review.ReviewState)
	require.Equal(t, "admin_approved", review.Resolution)
	require.Equal(t, int64(9), *review.ResolvedByUserID)
	require.NoError(t, mock.ExpectationsWereMet())
}
