package sharing

import (
	"strings"

	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListPrivateGroups(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListPrivateGroups(c.Request.Context(), userID, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

func (h *Handler) CreatePrivateGroup(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	var payload struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Platform    string `json:"platform"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid private group request")
		return
	}
	group, created, err := h.service.CreatePrivateGroup(c.Request.Context(), CreatePrivateGroupRequest{
		OwnerUserID: userID, Name: strings.TrimSpace(payload.Name), Description: strings.TrimSpace(payload.Description),
		Platform: strings.TrimSpace(payload.Platform), IdempotencyKey: strings.TrimSpace(c.GetHeader("Idempotency-Key")),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if created {
		response.Created(c, group)
		return
	}
	response.Success(c, group)
}

func (h *Handler) ListPrivateGroupMembers(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	groupID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListPrivateGroupMembers(c.Request.Context(), userID, groupID, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

func (h *Handler) GrantPrivateGroupMember(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	groupID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	var payload struct {
		UserID int64 `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid private group member request")
		return
	}
	if err := h.service.GrantPrivateGroupMember(c.Request.Context(), PrivateGroupMemberRequest{OwnerUserID: userID, GroupID: groupID, UserID: payload.UserID}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"group_id": groupID, "user_id": payload.UserID})
}

func (h *Handler) RevokePrivateGroupMember(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	groupID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	memberID, ok := positiveID(c, "user_id")
	if !ok {
		return
	}
	if err := h.service.RevokePrivateGroupMember(c.Request.Context(), PrivateGroupMemberRequest{OwnerUserID: userID, GroupID: groupID, UserID: memberID}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(204)
}

func (h *Handler) ArchivePrivateGroup(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	groupID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	if err := h.service.ArchivePrivateGroup(c.Request.Context(), userID, groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(204)
}
