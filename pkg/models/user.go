package models

import (
	"errors"
	"strings"
	"time"
)

// UserRole defines RBAC permission level for a user.
type UserRole string

const (
	RoleAdmin  UserRole = "admin"
	RoleEditor UserRole = "editor"
	RoleViewer UserRole = "viewer"
)

// IsValid checks if the UserRole is recognized.
func (r UserRole) IsValid() bool {
	return r == RoleAdmin || r == RoleEditor || r == RoleViewer
}

// User represents an authenticated MonStack-Go account with RBAC role and optional API key.
type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Role         UserRole   `json:"role"`
	APIKey       string     `json:"api_key,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLogin    *time.Time `json:"last_login,omitempty"`
}

// Validate checks required User fields.
func (u *User) Validate() error {
	if strings.TrimSpace(u.Username) == "" {
		return errors.New("username cannot be empty")
	}
	if !u.Role.IsValid() {
		return errors.New("invalid user role: must be admin, editor, or viewer")
	}
	return nil
}

// AuditEntry records a configuration or state-changing action for compliance and debugging.
type AuditEntry struct {
	ID         int64     `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`   // create, update, delete, reload, command
	Resource   string    `json:"resource"` // target, alert_rule, silence, agent, user, config
	ResourceID string    `json:"resource_id,omitempty"`
	Details    string    `json:"details,omitempty"`
	IPAddress  string    `json:"ip_address,omitempty"`
}
