// Package operations contains Redstone's operating workflows. It owns only
// operating records; sub2 remains the source of truth for users, announcements,
// accounts, groups, API keys, payment orders, and content-moderation engines.
package operations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
)

var (
	ErrUnavailable         = errors.New("operations service is unavailable")
	ErrInvalidInput        = errors.New("operations input is invalid")
	ErrNotFound            = errors.New("operations record was not found")
	ErrForbidden           = errors.New("operations access is forbidden")
	ErrConflict            = errors.New("operations request conflicts with existing state")
	ErrCampaignUnavailable = errors.New("campaign is not available")
)

const monetaryScale int32 = 8

type Withdrawal struct {
	ID                int64           `json:"id"`
	UserID            int64           `json:"user_id"`
	Amount            decimal.Decimal `json:"amount"`
	FeeAmount         decimal.Decimal `json:"fee_amount"`
	TotalDebited      decimal.Decimal `json:"total_debited"`
	PayoutMethod      string          `json:"payout_method"`
	Status            string          `json:"status"`
	AdminNote         string          `json:"admin_note"`
	ProcessedByUserID *int64          `json:"processed_by_user_id,omitempty"`
	ProcessedAt       *time.Time      `json:"processed_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type InvoiceProfile struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	InvoiceType    string    `json:"invoice_type"`
	TitleName      string    `json:"title_name"`
	TaxID          string    `json:"tax_id"`
	RecipientEmail string    `json:"recipient_email"`
	IsDefault      bool      `json:"is_default"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type InvoiceRequest struct {
	ID            int64           `json:"id"`
	RequestNumber string          `json:"request_number"`
	UserID        int64           `json:"user_id"`
	ProfileID     *int64          `json:"profile_id,omitempty"`
	Amount        decimal.Decimal `json:"amount"`
	Currency      string          `json:"currency"`
	SourceType    string          `json:"source_type"`
	SourceID      string          `json:"source_id"`
	Status        string          `json:"status"`
	InvoiceNumber string          `json:"invoice_number"`
	FileReference string          `json:"file_reference,omitempty"`
	Note          string          `json:"note"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type Ticket struct {
	ID              int64     `json:"id"`
	UserID          int64     `json:"user_id"`
	Subject         string    `json:"subject"`
	Category        string    `json:"category"`
	Status          string    `json:"status"`
	Priority        string    `json:"priority"`
	AssignedAdminID *int64    `json:"assigned_admin_id,omitempty"`
	LastMessageAt   time.Time `json:"last_message_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type TicketMessage struct {
	ID           int64     `json:"id"`
	TicketID     int64     `json:"ticket_id"`
	SenderKind   string    `json:"sender_kind"`
	SenderUserID *int64    `json:"sender_user_id,omitempty"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"created_at"`
}

type Campaign struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Status       string          `json:"status"`
	StartsAt     time.Time       `json:"starts_at"`
	EndsAt       time.Time       `json:"ends_at"`
	RewardAmount decimal.Decimal `json:"reward_amount"`
	MaxClaims    int             `json:"max_claims"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type ContentCase struct {
	ID              int64      `json:"id"`
	ReporterUserID  *int64     `json:"reporter_user_id,omitempty"`
	SubjectType     string     `json:"subject_type"`
	SubjectID       string     `json:"subject_id"`
	Reason          string     `json:"reason"`
	Status          string     `json:"status"`
	DecisionNote    string     `json:"decision_note"`
	DecidedByUserID *int64     `json:"decided_by_user_id,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type ProxyOption struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	OwnerUserID  *int64 `json:"owner_user_id,omitempty"`
	MaxAccounts  int    `json:"max_accounts"`
	AccountCount int64  `json:"account_count"`
}

type WithdrawalRequest struct {
	UserID          int64
	Amount          decimal.Decimal
	FeeAmount       decimal.Decimal
	PayoutMethod    string
	PayoutReference string
	IdempotencyKey  string
}

type InvoiceProfileRequest struct {
	UserID         int64
	InvoiceType    string
	TitleName      string
	TaxID          string
	RecipientEmail string
	IsDefault      bool
}

type InvoiceRequestInput struct {
	UserID       int64
	ProfileID    int64
	PaymentRefID string
}

type TicketRequest struct {
	UserID   int64
	Subject  string
	Category string
	Body     string
}

type CampaignRequest struct {
	AdminUserID  int64
	Name         string
	Description  string
	StartsAt     time.Time
	EndsAt       time.Time
	RewardAmount decimal.Decimal
	MaxClaims    int
}

// Service keeps each operating monetary mutation in the same database
// transaction as its business state via WalletService's executor extension.
type Service struct {
	db     *sql.DB
	wallet *wallet.Service
}

func NewService(db *sql.DB, walletService *wallet.Service) (*Service, error) {
	if db == nil || walletService == nil {
		return nil, ErrUnavailable
	}
	return &Service{db: db, wallet: walletService}, nil
}

func validMoney(v decimal.Decimal) bool {
	return v.IsPositive() && v.Equal(v.Round(monetaryScale))
}

func validText(v string, limit int) bool {
	v = strings.TrimSpace(v)
	return v != "" && len(v) <= limit
}

func requestFingerprint(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func operationKey(prefix string, id int64) string {
	return fmt.Sprintf("operations-%s-%d", prefix, id)
}

func (s *Service) require() error {
	if s == nil || s.db == nil || s.wallet == nil {
		return ErrUnavailable
	}
	return nil
}

func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
