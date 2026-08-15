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

func (h *Handler) AdminGetRuntimeHealth(c *gin.Context) {
	if !marketAdminSubject(c, h) {
		return
	}
	response.Success(c, h.service.MarketplaceRuntimeHealth(c.Request.Context()))
}

func (h *Handler) AdminGetFeePolicy(c *gin.Context) {
	if !marketAdminSubject(c, h) {
		return
	}
	policy, err := h.service.GetFeePolicy(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, policy)
}

func (h *Handler) AdminUpdateFeePolicy(c *gin.Context) {
	subject, ok := marketAdminSubjectValue(c, h)
	if !ok {
		return
	}
	var payload struct {
		UserServiceFeeRate decimal.Decimal `json:"user_service_fee_rate"`
		Reason             string          `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid marketplace fee policy")
		return
	}
	policy, err := h.service.UpdateFeePolicy(c.Request.Context(), UpdateFeePolicyRequest{
		ActorUserID: subject.UserID, UserServiceFeeRate: payload.UserServiceFeeRate, Reason: strings.TrimSpace(payload.Reason),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, policy)
}

func (h *Handler) CreateReport(c *gin.Context) {
	subject, ok := marketSellerSubject(c)
	if !ok || !marketHandlerAvailable(c, h) {
		return
	}
	productID, ok := sellerProductID(c)
	if !ok {
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid marketplace report")
		return
	}
	report, err := h.service.CreateReport(c.Request.Context(), CreateReportRequest{
		ReporterUserID: subject.UserID,
		ProductID:      productID,
		Reason:         strings.TrimSpace(payload.Reason),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, report)
}

func (h *Handler) AdminListOpenReports(c *gin.Context) {
	if !marketAdminSubject(c, h) {
		return
	}
	page, pageSize := response.ParsePagination(c)
	reports, total, err := h.service.ListOpenReports(c.Request.Context(), pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, reports, int64(total), page, pageSize)
}

func (h *Handler) AdminDismissReport(c *gin.Context) {
	h.adminResolveReport(c, reportResolutionDismiss)
}

func (h *Handler) AdminSuspendReport(c *gin.Context) {
	h.adminResolveReport(c, reportResolutionSuspend)
}

func (h *Handler) AdminReleaseReportHold(c *gin.Context) {
	h.adminResolveReport(c, reportResolutionRelease)
}

func (h *Handler) AdminListOpenContentReviews(c *gin.Context) {
	if !marketAdminSubject(c, h) {
		return
	}
	page, pageSize := response.ParsePagination(c)
	reviews, total, err := h.service.ListOpenContentReviews(c.Request.Context(), pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, reviews, int64(total), page, pageSize)
}

func (h *Handler) AdminApproveContentReview(c *gin.Context) {
	h.adminResolveContentReview(c, contentReviewActionApprove)
}

func (h *Handler) AdminRejectContentReview(c *gin.Context) {
	h.adminResolveContentReview(c, contentReviewActionReject)
}

func (h *Handler) adminResolveContentReview(c *gin.Context, action string) {
	subject, ok := marketAdminSubjectValue(c, h)
	if !ok {
		return
	}
	reviewID, ok := marketPositiveID(c, "id", "Invalid content review ID")
	if !ok {
		return
	}
	var payload struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid marketplace content review")
		return
	}
	review, err := h.service.ResolveContentReview(c.Request.Context(), ResolveContentReviewRequest{
		ActorUserID: subject.UserID, ReviewID: reviewID, Action: action, Note: strings.TrimSpace(payload.Note),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, review)
}

func (h *Handler) adminResolveReport(c *gin.Context, action string) {
	subject, ok := marketAdminSubjectValue(c, h)
	if !ok {
		return
	}
	reportID, ok := marketPositiveID(c, "id", "Invalid report ID")
	if !ok {
		return
	}
	var payload struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid report resolution")
		return
	}
	report, err := h.service.ResolveReport(c.Request.Context(), ResolveReportRequest{
		ActorUserID: subject.UserID, ReportID: reportID, Action: action, Note: strings.TrimSpace(payload.Note),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, report)
}

func (h *Handler) AdminFreezeSeller(c *gin.Context) {
	h.adminSellerControl(c, true)
}

func (h *Handler) AdminUnfreezeSeller(c *gin.Context) {
	h.adminSellerControl(c, false)
}

func (h *Handler) adminSellerControl(c *gin.Context, freeze bool) {
	subject, ok := marketAdminSubjectValue(c, h)
	if !ok {
		return
	}
	sellerID, ok := marketPositiveID(c, "id", "Invalid seller ID")
	if !ok {
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid seller control request")
		return
	}
	request := SellerFreezeRequest{ActorUserID: subject.UserID, SellerUserID: sellerID, Reason: strings.TrimSpace(payload.Reason)}
	var err error
	if freeze {
		err = h.service.FreezeSeller(c.Request.Context(), request)
	} else {
		err = h.service.UnfreezeSeller(c.Request.Context(), request)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"seller_user_id": sellerID, "frozen": freeze})
}

func (h *Handler) AdminReverseOrder(c *gin.Context) {
	subject, ok := marketAdminSubjectValue(c, h)
	if !ok {
		return
	}
	orderID, ok := marketPositiveID(c, "id", "Invalid order ID")
	if !ok {
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid order reversal")
		return
	}
	result, err := h.service.ReverseOrder(c.Request.Context(), ReverseOrderRequest{
		ActorUserID: subject.UserID, OrderID: orderID, Reason: strings.TrimSpace(payload.Reason),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *Handler) AdminCreateOfficialProduct(c *gin.Context) {
	subject, ok := marketAdminSubjectValue(c, h)
	if !ok {
		return
	}
	payload, ok := bindSellerProductPayload(c)
	if !ok {
		return
	}
	product, err := h.service.CreateOfficialProduct(c.Request.Context(), CreateOfficialProductRequest{
		SellerUserID: subject.UserID, ProductType: payload.ProductType, Title: payload.Title,
		Description: payload.Description, UnitPrice: payload.UnitPrice,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, product)
}

func (h *Handler) AdminListOfficialProducts(c *gin.Context) {
	subject, ok := marketAdminSubjectValue(c, h)
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	products, total, err := h.service.ListOfficialProducts(c.Request.Context(), subject.UserID, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, products, int64(total), page, pageSize)
}

func (h *Handler) AdminUploadOfficialDelivery(c *gin.Context) {
	subject, ok := marketAdminSubjectValue(c, h)
	if !ok {
		return
	}
	productID, ok := marketPositiveID(c, "id", "Invalid product ID")
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
	product, err := h.service.UploadOfficialDelivery(c.Request.Context(), UploadSellerDeliveryRequest{
		SellerUserID: subject.UserID, ProductID: productID, ContentType: contentType, Content: content,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, product)
}

func marketAdminSubject(c *gin.Context, h *Handler) bool {
	_, ok := marketAdminSubjectValue(c, h)
	return ok
}

func marketAdminSubjectValue(c *gin.Context, h *Handler) (middleware.AuthSubject, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Administrator not authenticated")
		return middleware.AuthSubject{}, false
	}
	if !marketHandlerAvailable(c, h) {
		return middleware.AuthSubject{}, false
	}
	return subject, true
}

func marketPositiveID(c *gin.Context, name, message string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, message)
		return 0, false
	}
	return id, true
}
