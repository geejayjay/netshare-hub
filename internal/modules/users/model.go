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
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateInput is what the service needs to create a user.
type CreateInput struct {
	Username    string
	DisplayName string
	Role        rbac.Role
	Password    string
}

// UpdateInput changes only the fields that are set (non-nil).
type UpdateInput struct {
	DisplayName *string
	Role        *rbac.Role
	Disabled    *bool
	Password    *string
}
