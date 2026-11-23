// Package rbac is the single source of truth for who may do what.
//
// To add a permission: add a constant, then add it to the roles that get it.
// To protect a route: set Access: httpx.Require(rbac.SomePermission).
// Nothing else needs to change; the frontend receives the user's permission
// list from GET /auth/me.
package rbac

import "slices"

// Permission is "<resource>:<action>".
type Permission string

const (
	UsersRead     Permission = "users:read"
	UsersWrite    Permission = "users:write"
	GroupsRead    Permission = "groups:read"
	GroupsWrite   Permission = "groups:write"
	SharesRead    Permission = "shares:read"
	SharesWrite   Permission = "shares:write"
	FilesRead     Permission = "files:read"
	FilesWrite    Permission = "files:write"
	SettingsRead  Permission = "settings:read"
	SettingsWrite Permission = "settings:write"
	AuditRead     Permission = "audit:read"
	SystemRead    Permission = "system:read"
	// JobsManage lets a user see and cancel every user's background jobs.
	// Without it, users only see and cancel their own.
	JobsManage Permission = "jobs:manage"
	// AlertsManage lets a user acknowledge and remove server alerts.
	// Seeing them needs SystemRead.
	AlertsManage Permission = "alerts:manage"
	// SessionsManage lets a user end other people's SMB sessions.
	SessionsManage Permission = "sessions:manage"
)

// all lists every permission. Add new ones here too: the Admin role gets the
// whole list, and the frontend's Permission type is generated from it.
var all = []Permission{
	UsersRead, UsersWrite, GroupsRead, GroupsWrite, SharesRead, SharesWrite,
	FilesRead, FilesWrite, SettingsRead, SettingsWrite, AuditRead, SystemRead,
	JobsManage, AlertsManage, SessionsManage,
}

// Role is a named bundle of permissions.
type Role string

const (
	RoleAdmin        Role = "admin"
	RoleShareManager Role = "share_manager"
	RoleUser         Role = "user"
	RoleAuditor      Role = "auditor"
)

// RoleInfo describes a role for the UI.
type RoleInfo struct {
	ID          Role         `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions" nullable:"false"`
}

// roles is the whole policy. Edit this table to change what a role can do.
var roles = []RoleInfo{
	{
		ID: RoleAdmin, Name: "Admin",
		Description: "Full access, including users, settings and the audit log.",
		Permissions: all,
	},
	{
		ID: RoleShareManager, Name: "Share Manager",
		Description: "Manages shares and group membership. Cannot manage admins or settings.",
		Permissions: []Permission{
			UsersRead, GroupsRead, GroupsWrite, SharesRead, SharesWrite, FilesRead, FilesWrite, SystemRead,
		},
	},
	{
		ID: RoleUser, Name: "User",
		Description: "Uses the file manager on shares they have access to.",
		Permissions: []Permission{FilesRead, FilesWrite},
	},
	{
		ID: RoleAuditor, Name: "Read-only Auditor",
		Description: "Can view everything and change nothing.",
		Permissions: []Permission{
			UsersRead, GroupsRead, SharesRead, FilesRead, SettingsRead, AuditRead, SystemRead,
		},
	},
}

// Roles returns every role, in display order.
func Roles() []RoleInfo { return roles }

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return slices.ContainsFunc(roles, func(i RoleInfo) bool { return i.ID == r })
}

// Permissions returns the permissions granted to a role.
func Permissions(r Role) []Permission {
	for _, i := range roles {
		if i.ID == r {
			return i.Permissions
		}
	}
	return nil
}

// Can reports whether a role holds a permission.
func Can(r Role, p Permission) bool {
	return slices.Contains(Permissions(r), p)
}
