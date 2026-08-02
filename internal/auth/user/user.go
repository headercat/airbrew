// Package user holds the user domain model, persistence and service.
package user

import "time"

// Status is the lifecycle state of a user account.
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusDeleted   Status = "deleted"
)

// Role controls access to administrative surfaces.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// User is the core user record. PublicSubject is the OIDC 'sub'.
type User struct {
	ID            string
	PublicSubject string
	Email         string
	EmailVerified bool
	Status        Status
	Role          Role
	DisplayName   string
	Description   string
	Birthday      *time.Time
	PhoneNumber   string
	AvatarURL     string
	CustomFields  map[string]string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time
}
