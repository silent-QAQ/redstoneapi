package market

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Handler exposes marketplace listing, purchase, and current-user order APIs.
// Every identity is derived from the authenticated subject, never request JSON.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) ListProducts(c *gin.Context) {
	if _, ok := middleware.GetAuthSubjectFromContext(c); !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return
	}
	page, pageSize := response.ParsePagination(c)
	products, total, err := h.service.ListActiveProducts(c.Request.Context(), pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, products, int64(total), page, pageSize)
}

// CreateOrder handles POST /api/v1/market/products/:id/orders. A caller must
// supply Idempotency-Key because this endpoint debits normal wallet balance.
func (h *Handler) CreateOrder(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return
	}
	productID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || productID <= 0 {
		response.BadRequest(c, "Invalid product ID")
		return
	}
	if err := validateCreateOrderPayload(c); err != nil {
		response.BadRequest(c, "Invalid order request body")
		return
	}
	idempotencyKey := c.GetHeader("Idempotency-Key")
	if !validIdempotencyKey(idempotencyKey) {
		response.BadRequest(c, "Idempotency-Key is required and must be at most 128 characters")
		return
	}
	result, err := h.service.CreateOrder(c.Request.Context(), CreateOrderRequest{
		BuyerUserID:    subject.UserID,
		ProductID:      productID,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
		response.Success(c, result.Order)
		return
	}
	response.Created(c, result.Order)
}

// ListCurrentUserOrders handles GET /api/v1/market/orders. It exposes only
// orders purchased by the authenticated user; seller and admin views are
// separate operations with their own authorization boundaries.
func (h *Handler) ListCurrentUserOrders(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return
	}
	page, pageSize := response.ParsePagination(c)
	orders, total, err := h.service.ListOrdersByBuyer(c.Request.Context(), subject.UserID, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, orders, int64(total), page, pageSize)
}

// AdminListOrders returns operational order metadata for the protected admin
// queue. Delivery content remains inaccessible from this list.
func (h *Handler) AdminListOrders(c *gin.Context) {
	if !marketAdminSubject(c, h) {
		return
	}
	page, pageSize := response.ParsePagination(c)
	orders, total, err := h.service.ListAdminOrders(c.Request.Context(), pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, orders, int64(total), page, pageSize)
}

// AdminListOpenAppeals returns the explicit queue rather than relying on an
// operator to know an order ID before they can resolve a buyer dispute.
func (h *Handler) AdminListOpenAppeals(c *gin.Context) {
	if !marketAdminSubject(c, h) {
		return
	}
	page, pageSize := response.ParsePagination(c)
	appeals, total, err := h.service.ListOpenAppeals(c.Request.Context(), pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, appeals, int64(total), page, pageSize)
}

// DeliverOrder exposes a one-time buyer delivery. The repository enforces the
// buyer boundary and records the audit event before advancing settlement.
func (h *Handler) DeliverOrder(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return
	}
	orderID, ok := marketOrderID(c)
	if !ok {
		return
	}
	delivery, err := h.service.DeliverOrder(c.Request.Context(), subject.UserID, orderID, c.GetHeader("X-Request-ID"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, delivery)
}

// DownloadFileDelivery writes a one-time file delivery directly to the
// authenticated buyer response. The service performs the private decryption
// and atomic claim; this handler never returns object-storage URLs or embeds
// file content in a JSON envelope.
func (h *Handler) DownloadFileDelivery(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return
	}
	orderID, ok := marketOrderID(c)
	if !ok {
		return
	}
	delivery, err := h.service.DownloadFileDelivery(c.Request.Context(), subject.UserID, orderID, c.GetHeader("X-Request-ID"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	defer delivery.clear()
	c.Header("Cache-Control", "no-store, private")
	c.Header("Content-Disposition", "attachment")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(200, "application/octet-stream", delivery.content)
}

// CreateAppeal lets a buyer open a dispute for one of their own paid or
// delivered orders. The repository locks the order and enforces the one-open-
// appeal invariant, so neither an order ID nor a request body can be used to
// affect another buyer's order.
func (h *Handler) CreateAppeal(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return
	}
	orderID, ok := marketOrderID(c)
	if !ok {
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid appeal request")
		return
	}
	appeal, err := h.service.CreateAppeal(c.Request.Context(), CreateAppealRequest{
		BuyerUserID: subject.UserID,
		OrderID:     orderID,
		Reason:      strings.TrimSpace(payload.Reason),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, appeal)
}

// AdminSettleOrder manually releases an already-delivered order's seller
// proceeds. The route registration supplies admin authentication, compliance,
// audit logging, and step-up verification before this handler runs.
func (h *Handler) AdminSettleOrder(c *gin.Context) {
	result, ok := h.adminOrderAction(c, func(request AdminOrderRequest) (SettlementResult, error) {
		return h.service.SettleOrder(c.Request.Context(), request)
	})
	if ok {
		response.Success(c, result)
	}
}

// AdminRefundOrder returns an eligible order's entire purchase amount to its
// buyer. Financial and wallet changes remain one database transaction.
func (h *Handler) AdminRefundOrder(c *gin.Context) {
	result, ok := h.adminOrderAction(c, func(request AdminOrderRequest) (SettlementResult, error) {
		return h.service.RefundOrder(c.Request.Context(), request)
	})
	if ok {
		response.Success(c, result)
	}
}

// AdminResolveAppeal settles or refunds exactly one open buyer appeal. The
// chosen action is validated by the domain service rather than client input.
func (h *Handler) AdminResolveAppeal(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Administrator not authenticated")
		return
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return
	}
	orderID, ok := marketOrderID(c)
	if !ok {
		return
	}
	var payload struct {
		Action ResolveAppealAction `json:"action"`
		Note   string              `json:"note"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid appeal resolution request")
		return
	}
	result, err := h.service.ResolveAppeal(c.Request.Context(), ResolveAppealRequest{
		ActorUserID: subject.UserID,
		OrderID:     orderID,
		Action:      payload.Action,
		Note:        strings.TrimSpace(payload.Note),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *Handler) adminOrderAction(c *gin.Context, action func(AdminOrderRequest) (SettlementResult, error)) (SettlementResult, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Administrator not authenticated")
		return SettlementResult{}, false
	}
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return SettlementResult{}, false
	}
	orderID, ok := marketOrderID(c)
	if !ok {
		return SettlementResult{}, false
	}
	result, err := action(AdminOrderRequest{ActorUserID: subject.UserID, OrderID: orderID})
	if err != nil {
		response.ErrorFrom(c, err)
		return SettlementResult{}, false
	}
	return result, true
}

func marketOrderID(c *gin.Context) (int64, bool) {
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || orderID <= 0 {
		response.BadRequest(c, "Invalid order ID")
		return 0, false
	}
	return orderID, true
}

// validateCreateOrderPayload accepts no body or exactly an empty JSON object.
// Product ID comes from the path, while price and delivery item are selected
// under database locks; accepting client fields would create misleading or
// unsafe inputs that the order transaction must ignore.
func validateCreateOrderPayload(c *gin.Context) error {
	if c.Request == nil || c.Request.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(c.Request.Body)
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	if payload == nil || len(payload) != 0 {
		return io.ErrUnexpectedEOF
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}
