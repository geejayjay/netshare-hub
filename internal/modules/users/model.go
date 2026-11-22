// Package users manages NetShare Hub accounts.
//
// Layout of every module:
//
//	model.go    data types
//	errors.go   this module's error codes
//	repo.go     SQL only: no rules, no HTTP
//	service.go  business rules: no SQL, no HTTP
//	handler.go  HTTP only: request/response types and route table
package users

import (
	"context"
	"time"

	"netshare-hub/internal/platform/rbac"
)

// User is an account. The password hash is never part of this type, so it
// cannot leak into a response by accident.
type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	Role        rbac.Role `json:"role"`
	Disabled    bool      `json:"disabled"`
	SMBAccess   bool      `json:"smbAccess" doc:"True when the user has a Linux + Samba account and can be given access to shares. False means web login only."`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateInput is what the service needs to create a user.
type CreateInput struct {
	Username    string
	DisplayName string
	Role        rbac.Role
	Password    string
	SMBAccess   bool
}

// UpdateInput changes only the fields that are set (non-nil).
type UpdateInput struct {
	DisplayName *string
	Role        *rbac.Role
	Disabled    *bool
	Password    *string
	SMBAccess   *bool
}

// AccessRevoker removes a user from everything that grants them SMB access.
// Users declares it and the shares module implements it, so users does not
// depend on shares. It runs BEFORE a system account is removed: if it fails,
// the account stays, and a stale config can never grant access to a deleted
// (and possibly later re-created) username.
type AccessRevoker interface {
	RevokeUser(ctx context.Context, userID int64) error
}
