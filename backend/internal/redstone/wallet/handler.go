package wallet

import (
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const maxLedgerPageSize = 200

// Handler exposes only the authenticated user's wallet read model. Commands
// remain behind their dedicated business flows so a client cannot mutate a
// balance directly through this API.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type snapshotResponse struct {
	NormalBalance decimal.Decimal `json:"normal_balance"`
	BoundBalance  decimal.Decimal `json:"bound_balance"`
}

type ledgerEntryResponse struct {
	ID           int64             `json:"id"`
	Asset        AssetType         `json:"asset"`
	Operation    LedgerOperation   `json:"operation"`
	Delta        decimal.Decimal   `json:"delta"`
	BalanceAfter decimal.Decimal   `json:"balance_after"`
	Reference    referenceResponse `json:"reference"`
	CreatedAt    time.Time         `json:"created_at"`
}

type referenceResponse struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// GetSnapshot handles GET /api/v1/wallet.
func (h *Handler) GetSnapshot(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Wallet is unavailable")
		return
	}

	snapshot, err := h.service.GetSnapshot(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, snapshotResponse{
		NormalBalance: snapshot.Balances.Normal,
		BoundBalance:  snapshot.Balances.Bound,
	})
}

// ListLedger handles GET /api/v1/wallet/ledger.
func (h *Handler) ListLedger(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Wallet is unavailable")
		return
	}

	page, pageSize := response.ParsePagination(c)
	if pageSize > maxLedgerPageSize {
		response.BadRequest(c, "page_size must not exceed 200")
		return
	}
	ledger, err := h.service.ListLedger(c.Request.Context(), subject.UserID, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	entries := make([]ledgerEntryResponse, 0, len(ledger.Entries))
	for _, entry := range ledger.Entries {
		entries = append(entries, ledgerEntryResponse{
			ID:           entry.ID,
			Asset:        entry.Asset,
			Operation:    entry.Operation,
			Delta:        entry.Delta,
			BalanceAfter: entry.BalanceAfter,
			Reference:    referenceResponse{Type: entry.Reference.Type, ID: entry.Reference.ID},
			CreatedAt:    entry.CreatedAt,
		})
	}
	response.Paginated(c, entries, int64(ledger.Total), page, pageSize)
}
