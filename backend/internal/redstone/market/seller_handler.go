package market

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const maxSellerDeliveryUploadBytes int64 = 32 << 20

// GetSellerDashboard returns the authenticated seller's own normal-balance
// eligibility view. The response deliberately has no bound balance field.
func (h *Handler) GetSellerDashboard(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok {
		return
	}
	if !marketHandlerAvailable(c, h) {
		return
	}
	dashboard, err := h.service.GetSellerDashboard(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dashboard)
}

// ListCurrentSellerProducts exposes only products whose seller_user_id is the
// authenticated subject. It never uses a request-selected seller identifier.
func (h *Handler) ListCurrentSellerProducts(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok {
		return
	}
	if !marketHandlerAvailable(c, h) {
		return
	}
	page, pageSize := response.ParsePagination(c)
	products, total, err := h.service.ListSellerProducts(c.Request.Context(), subject.UserID, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, products, int64(total), page, pageSize)
}

// ListCurrentSellerOrders exposes sales and settlement history to the seller
// that owns the listing. It uses the authenticated subject only, so a seller
// cannot enumerate another seller's buyers or order history.
func (h *Handler) ListCurrentSellerOrders(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok || !marketHandlerAvailable(c, h) {
		return
	}
	page, pageSize := response.ParsePagination(c)
	orders, total, err := h.service.ListOrdersBySeller(c.Request.Context(), subject.UserID, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, orders, int64(total), page, pageSize)
}

type sellerProductPayload struct {
	ProductType string          `json:"product_type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	UnitPrice   decimal.Decimal `json:"unit_price"`
	AccountID   *int64          `json:"account_id,omitempty"`
}

func (h *Handler) CreateSellerDraft(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok {
		return
	}
	if !marketHandlerAvailable(c, h) {
		return
	}
	payload, ok := bindSellerProductPayload(c)
	if !ok {
		return
	}
	product, err := h.service.CreateSellerDraft(c.Request.Context(), CreateSellerDraftRequest{
		SellerUserID: subject.UserID,
		ProductType:  payload.ProductType,
		Title:        payload.Title,
		Description:  payload.Description,
		UnitPrice:    payload.UnitPrice,
		AccountID:    payload.AccountID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, product)
}

func (h *Handler) UpdateSellerDraft(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok {
		return
	}
	if !marketHandlerAvailable(c, h) {
		return
	}
	productID, ok := sellerProductID(c)
	if !ok {
		return
	}
	payload, ok := bindSellerProductPayload(c)
	if !ok {
		return
	}
	product, err := h.service.UpdateSellerDraft(c.Request.Context(), UpdateSellerDraftRequest{
		SellerUserID: subject.UserID,
		ProductID:    productID,
		ProductType:  payload.ProductType,
		Title:        payload.Title,
		Description:  payload.Description,
		UnitPrice:    payload.UnitPrice,
		AccountID:    payload.AccountID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, product)
}

func (h *Handler) PublishSellerProduct(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok {
		return
	}
	if !marketHandlerAvailable(c, h) {
		return
	}
	productID, ok := sellerProductID(c)
	if !ok {
		return
	}
	product, err := h.service.PublishSellerProduct(c.Request.Context(), PublishSellerProductRequest{
		SellerUserID: subject.UserID,
		ProductID:    productID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, product)
}

func (h *Handler) ArchiveSellerProduct(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok {
		return
	}
	if !marketHandlerAvailable(c, h) {
		return
	}
	productID, ok := sellerProductID(c)
	if !ok {
		return
	}
	product, err := h.service.ArchiveSellerProduct(c.Request.Context(), ArchiveSellerProductRequest{
		SellerUserID: subject.UserID,
		ProductID:    productID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, product)
}

// UploadSellerDelivery accepts one seller-owned inventory item. Plaintext is
// confined to this request buffer, zeroed by the service, and is never stored
// in request DTOs, logs, or database rows.
func (h *Handler) UploadSellerDelivery(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok || !marketHandlerAvailable(c, h) {
		return
	}
	productID, ok := sellerProductID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSellerDeliveryUploadBytes)
	file, header, err := c.Request.FormFile("content")
	if err != nil {
		response.BadRequest(c, "Invalid marketplace delivery upload")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxSellerDeliveryUploadBytes+1))
	if err != nil || int64(len(content)) > maxSellerDeliveryUploadBytes {
		response.BadRequest(c, "Invalid marketplace delivery upload")
		return
	}
	contentType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = http.DetectContentType(content)
	}
	product, err := h.service.UploadSellerDelivery(c.Request.Context(), UploadSellerDeliveryRequest{
		SellerUserID: subject.UserID, ProductID: productID, ContentType: contentType, Content: content,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, product)
}

func marketSellerSubject(c *gin.Context) (middleware.AuthSubject, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return middleware.AuthSubject{}, false
	}
	return subject, true
}

func marketHandlerAvailable(c *gin.Context, h *Handler) bool {
	if h == nil || h.service == nil {
		response.Error(c, 503, "Marketplace is unavailable")
		return false
	}
	return true
}

func sellerProductID(c *gin.Context) (int64, bool) {
	productID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || productID <= 0 {
		response.BadRequest(c, "Invalid product ID")
		return 0, false
	}
	return productID, true
}

func bindSellerProductPayload(c *gin.Context) (sellerProductPayload, bool) {
	var payload sellerProductPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid marketplace seller product request")
		return sellerProductPayload{}, false
	}
	payload.ProductType = strings.TrimSpace(payload.ProductType)
	payload.Title = strings.TrimSpace(payload.Title)
	payload.Description = strings.TrimSpace(payload.Description)
	return payload, true
}
