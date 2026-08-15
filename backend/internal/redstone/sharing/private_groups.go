package sharing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidPrivateGroup       = errors.New("account share private group is invalid")
	ErrPrivateGroupNotFound      = errors.New("account share private group was not found")
	ErrPrivateGroupForbidden     = errors.New("account share private group access is forbidden")
	ErrPrivateGroupConflict      = errors.New("account share private group request conflicts with existing state")
	ErrPrivateGroupUnavailable   = errors.New("account share private group repository is unavailable")
	ErrPrivateGroupOwnerRequired = errors.New("account share private group owner membership is required")
	ErrPrivateGroupRequired      = errors.New("account share room requires an active private group")
)

type PrivateGroupStatus string

const (
	PrivateGroupActive   PrivateGroupStatus = "active"
	PrivateGroupArchived PrivateGroupStatus = "archived"
)

// PrivateGroup is Redstone ownership metadata around an existing sub2
// exclusive group. Group scheduling and API key validation remain upstream.
type PrivateGroup struct {
	GroupID     int64              `json:"group_id"`
	OwnerUserID int64              `json:"owner_user_id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Platform    string             `json:"platform"`
	Status      PrivateGroupStatus `json:"status"`
	MemberCount int                `json:"member_count"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type PrivateGroupMember struct {
	GroupID   int64      `json:"group_id"`
	UserID    int64      `json:"user_id"`
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	GrantedAt time.Time  `json:"granted_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type CreatePrivateGroupRequest struct {
	OwnerUserID    int64
	Name           string
	Description    string
	Platform       string
	IdempotencyKey string
}

func (r CreatePrivateGroupRequest) Validate() error {
	if r.OwnerUserID <= 0 || !validText(r.Name, 60) || !validText(r.Platform, 50) ||
		len(r.Description) > 2000 || !validKey(r.IdempotencyKey) {
		return ErrInvalidPrivateGroup
	}
	return nil
}

type PrivateGroupMemberRequest struct {
	OwnerUserID int64
	GroupID     int64
	UserID      int64
}

func (r PrivateGroupMemberRequest) Validate() error {
	if r.OwnerUserID <= 0 || r.GroupID <= 0 || r.UserID <= 0 {
		return ErrInvalidPrivateGroup
	}
	return nil
}

// PrivateGroupRepository is deliberately narrow. It persists ownership and
// member lifecycle, then delegates group authorization to sub2's existing
// groups and user_allowed_groups tables.
type PrivateGroupRepository interface {
	CreatePrivateGroup(context.Context, CreatePrivateGroupRequest) (PrivateGroup, bool, error)
	ListPrivateGroups(context.Context, int64, int, int) ([]PrivateGroup, int, error)
	ListPrivateGroupMembers(context.Context, int64, int64, int, int) ([]PrivateGroupMember, int, error)
	GrantPrivateGroupMember(context.Context, PrivateGroupMemberRequest) error
	RevokePrivateGroupMember(context.Context, PrivateGroupMemberRequest) error
	ArchivePrivateGroup(context.Context, int64, int64) error
}

func (s *Service) privateGroupRepository() (PrivateGroupRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrPrivateGroupUnavailable
	}
	repository, ok := s.repository.(PrivateGroupRepository)
	if !ok {
		return nil, ErrPrivateGroupUnavailable
	}
	return repository, nil
}

func (s *Service) CreatePrivateGroup(ctx context.Context, request CreatePrivateGroupRequest) (PrivateGroup, bool, error) {
	if err := request.Validate(); err != nil {
		return PrivateGroup{}, false, applicationError(err)
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Description = strings.TrimSpace(request.Description)
	request.Platform = strings.TrimSpace(request.Platform)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	repository, err := s.privateGroupRepository()
	if err != nil {
		return PrivateGroup{}, false, applicationError(err)
	}
	group, created, err := repository.CreatePrivateGroup(ctx, request)
	return group, created, applicationError(err)
}

func (s *Service) ListPrivateGroups(ctx context.Context, ownerUserID int64, limit, offset int) ([]PrivateGroup, int, error) {
	if ownerUserID <= 0 || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidPrivateGroup)
	}
	repository, err := s.privateGroupRepository()
	if err != nil {
		return nil, 0, applicationError(err)
	}
	items, total, err := repository.ListPrivateGroups(ctx, ownerUserID, limit, offset)
	return items, total, applicationError(err)
}

func (s *Service) ListPrivateGroupMembers(ctx context.Context, ownerUserID, groupID int64, limit, offset int) ([]PrivateGroupMember, int, error) {
	if ownerUserID <= 0 || groupID <= 0 || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidPrivateGroup)
	}
	repository, err := s.privateGroupRepository()
	if err != nil {
		return nil, 0, applicationError(err)
	}
	items, total, err := repository.ListPrivateGroupMembers(ctx, ownerUserID, groupID, limit, offset)
	return items, total, applicationError(err)
}

func (s *Service) GrantPrivateGroupMember(ctx context.Context, request PrivateGroupMemberRequest) error {
	if err := request.Validate(); err != nil {
		return applicationError(err)
	}
	repository, err := s.privateGroupRepository()
	if err != nil {
		return applicationError(err)
	}
	return applicationError(repository.GrantPrivateGroupMember(ctx, request))
}

func (s *Service) RevokePrivateGroupMember(ctx context.Context, request PrivateGroupMemberRequest) error {
	if err := request.Validate(); err != nil {
		return applicationError(err)
	}
	repository, err := s.privateGroupRepository()
	if err != nil {
		return applicationError(err)
	}
	return applicationError(repository.RevokePrivateGroupMember(ctx, request))
}

func (s *Service) ArchivePrivateGroup(ctx context.Context, ownerUserID, groupID int64) error {
	if ownerUserID <= 0 || groupID <= 0 {
		return applicationError(ErrInvalidPrivateGroup)
	}
	repository, err := s.privateGroupRepository()
	if err != nil {
		return applicationError(err)
	}
	return applicationError(repository.ArchivePrivateGroup(ctx, ownerUserID, groupID))
}

func privateGroupFingerprint(request CreatePrivateGroupRequest) string {
	return fmt.Sprintf("%s\n%s\n%s", request.Name, request.Description, request.Platform)
}
