package controlledaccount

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// TriggerVerification 手动触发账号验真
// POST /api/v1/redstone/accounts/:id/verify
func (h *Handler) TriggerVerification(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	accountIDStr := c.Param("id")
	accountID, err := strconv.ParseInt(accountIDStr, 10, 64)
	if err != nil || accountID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account id"})
		return
	}

	// 检查账号归属
	owns, err := h.service.OwnsAccount(c.Request.Context(), userID.(int64), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check ownership"})
		return
	}
	if !owns {
		c.JSON(http.StatusForbidden, gin.H{"error": "account not found or not owned by you"})
		return
	}

	// 触发验真
	if h.verifier == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "verification service is not available"})
		return
	}

	result, err := h.verifier.VerifyAccount(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "verification failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"account_id": accountID,
		"result":     result,
		"message":    "verification completed",
	})
}

// GetVerificationHistory 获取账号验真历史
// GET /api/v1/redstone/accounts/:id/verify/history
func (h *Handler) GetVerificationHistory(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	accountIDStr := c.Param("id")
	accountID, err := strconv.ParseInt(accountIDStr, 10, 64)
	if err != nil || accountID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account id"})
		return
	}

	// 检查账号归属
	owns, err := h.service.OwnsAccount(c.Request.Context(), userID.(int64), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check ownership"})
		return
	}
	if !owns {
		c.JSON(http.StatusForbidden, gin.H{"error": "account not found or not owned by you"})
		return
	}

	// 获取验真历史
	history, err := h.getVerificationHistory(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get history"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"account_id": accountID,
		"history":    history,
	})
}

type VerificationHistoryRecord struct {
	ID         int64     `json:"id"`
	AccountID  int64     `json:"account_id"`
	Score      int       `json:"score"`
	Verdict    string    `json:"verdict"`
	Details    string    `json:"details,omitempty"`
	VerifiedAt time.Time `json:"verified_at"`
}

func (h *Handler) getVerificationHistory(ctx context.Context, accountID int64) ([]VerificationHistoryRecord, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, account_id, verify_score, verify_verdict, verify_details, verified_at
		FROM redstone_account_verification_history
		WHERE account_id = $1
		ORDER BY verified_at DESC
		LIMIT 50
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []VerificationHistoryRecord
	for rows.Next() {
		var record VerificationHistoryRecord
		if err := rows.Scan(
			&record.ID,
			&record.AccountID,
			&record.Score,
			&record.Verdict,
			&record.Details,
			&record.VerifiedAt,
		); err != nil {
			return nil, err
		}
		history = append(history, record)
	}

	return history, rows.Err()
}

// GetVerificationStats 获取全局验真统计（管理员）
// GET /api/v1/admin/accounts/verify/stats
func (h *Handler) GetVerificationStats(c *gin.Context) {
	// 检查管理员权限
	isAdmin, exists := c.Get("is_admin")
	if !exists || !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin access required"})
		return
	}

	stats, err := h.getVerificationStats(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get stats"})
		return
	}

	c.JSON(http.StatusOK, stats)
}

type VerificationStats struct {
	TotalAccounts       int64     `json:"total_accounts"`
	VerifiedAccounts    int64     `json:"verified_accounts"`
	PassedAccounts      int64     `json:"passed_accounts"`
	FailedAccounts      int64     `json:"failed_accounts"`
	FrozenAccounts      int64     `json:"frozen_accounts"`
	LastVerificationRun time.Time `json:"last_verification_run"`
	AvgScore            float64   `json:"avg_score"`
}

func (h *Handler) getVerificationStats(ctx context.Context) (*VerificationStats, error) {
	var stats VerificationStats

	err := h.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			COUNT(r.last_verified_at) as verified,
			COUNT(CASE WHEN r.verify_verdict = 'pass' THEN 1 END) as passed,
			COUNT(CASE WHEN r.verify_verdict = 'fail' THEN 1 END) as failed,
			COUNT(CASE WHEN r.lifecycle = 'frozen' THEN 1 END) as frozen,
			MAX(r.last_verified_at) as last_run,
			COALESCE(AVG(r.verify_score), 0) as avg_score
		FROM redstone_user_controlled_accounts r
		WHERE r.lifecycle <> 'revoked'
	`).Scan(
		&stats.TotalAccounts,
		&stats.VerifiedAccounts,
		&stats.PassedAccounts,
		&stats.FailedAccounts,
		&stats.FrozenAccounts,
		&stats.LastVerificationRun,
		&stats.AvgScore,
	)

	return &stats, err
}
