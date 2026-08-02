// Package contact holds the contacts (address book) domain model, persistence
// and service: a user's contacts (each a vCard-style record with structured
// name, multi-value emails/phones/addresses/ims/urls, notes and an optional
// avatar) plus user-defined groups (labels) with many-to-many membership.
//
// Avatars are stored in the blob store (namespace "contacts") and referenced by
// avatar_path. The package is a leaf: only the contacts module facade and its
// handler package depend on it.
package contact

import (
	"errors"
	"time"
)

// Sentinel errors.
var (
	// ErrNotFound is returned when no contact matches the lookup.
	ErrNotFound = errors.New("contacts: contact not found")
	// ErrGroupNotFound is returned when no group matches the lookup.
	ErrGroupNotFound = errors.New("contacts: group not found")
	// ErrNameRequired is returned when a contact has no name at all.
	ErrNameRequired = errors.New("contacts: name required")
	// ErrGroupNameRequired is returned when a group name is empty.
	ErrGroupNameRequired = errors.New("contacts: group name required")
	// ErrGroupNameTaken is returned when a group name already exists for the user.
	ErrGroupNameTaken = errors.New("contacts: group name taken")
	// ErrInvalidInput is returned on shape-validation failure.
	ErrInvalidInput = errors.New("contacts: invalid input")
)

// Type is a loose label carried on multi-value fields (email/phone/...). It is
// a free-form string (home, work, mobile, ...) so vCard TYPE parameters
// round-trip without a fixed enum. The empty string means "unspecified".
type Type string

// Email is one email entry on a contact.
type Email struct {
	Value string `json:"value"`
	Type  Type   `json:"type,omitempty"`
}

// Phone is one phone entry on a contact.
type Phone struct {
	Value string `json:"value"`
	Type  Type   `json:"type,omitempty"`
}

// IM is one instant-messaging handle on a contact.
type IM struct {
	Value string `json:"value"`
	Type  Type   `json:"type,omitempty"`
}

// URL is one web address on a contact.
type URL struct {
	Value string `json:"value"`
	Type  Type   `json:"type,omitempty"`
}

// Address is one postal address on a contact.
type Address struct {
	Type       Type   `json:"type,omitempty"`
	Street     string `json:"street,omitempty"`
	Locality   string `json:"locality,omitempty"` // city
	Region     string `json:"region,omitempty"`   // state/province
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
}

// Contact is one address-book entry owned by a user.
type Contact struct {
	ID          string
	UserID      string
	NamePrefix  string
	GivenName   string
	MiddleName  string
	FamilyName  string
	NameSuffix  string
	DisplayName string
	Nickname    string
	Company     string
	Title       string
	Department  string
	Emails      []Email
	Phones      []Phone
	Addresses   []Address
	IMs         []IM
	URLs        []URL
	Birthday    *time.Time
	Notes       string
	AvatarPath  string
	IsFavorite  bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SortName returns the value used to sort and label the contact in listings: it
// prefers display_name, then "Given Family", then the first email, falling back
// to nickname.
func (c *Contact) SortName() string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	if c.GivenName != "" || c.FamilyName != "" {
		return joinNames(c.GivenName, c.FamilyName)
	}
	for _, e := range c.Emails {
		if e.Value != "" {
			return e.Value
		}
	}
	if c.Nickname != "" {
		return c.Nickname
	}
	return ""
}

func joinNames(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += p
	}
	return out
}

// Group is a user-defined label that contacts can belong to (many-to-many).
type Group struct {
	ID        string
	UserID    string
	Name      string
	Color     string
	Count     int // populated by list-with-count queries; 0 otherwise
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateContactInput carries the editable fields for creating a contact.
type CreateContactInput struct {
	UserID      string
	NamePrefix  string
	GivenName   string
	MiddleName  string
	FamilyName  string
	NameSuffix  string
	DisplayName string
	Nickname    string
	Company     string
	Title       string
	Department  string
	Emails      []Email
	Phones      []Phone
	Addresses   []Address
	IMs         []IM
	URLs        []URL
	Birthday    *time.Time
	Notes       string
	IsFavorite  bool
	GroupIDs    []string
}

// UpdateContactInput carries the editable fields for a full replacement (PUT).
type UpdateContactInput = CreateContactInput

// ContactPatch carries optional fields for a partial update (PATCH). A nil
// pointer means "leave unchanged"; for slices a nil pointer means unchanged
// while a non-nil empty slice clears the field.
type ContactPatch struct {
	NamePrefix  *string
	GivenName   *string
	MiddleName  *string
	FamilyName  *string
	NameSuffix  *string
	DisplayName *string
	Nickname    *string
	Company     *string
	Title       *string
	Department  *string
	Emails      *[]Email
	Phones      *[]Phone
	Addresses   *[]Address
	IMs         *[]IM
	URLs        *[]URL
	Birthday    *time.Time // nil = clear; use BirthdaySet=false to leave unchanged
	BirthdaySet bool
	Notes       *string
	IsFavorite  *bool
}

// CreateGroupInput carries the editable fields for creating a group.
type CreateGroupInput struct {
	UserID string
	Name   string
	Color  string
}

// ListFilter controls which contacts are returned.
type ListFilter struct {
	UserID   string
	GroupID  string // "" = any
	Favorite bool   // only favorites
	Search   string // matches across name/email/phone/company/notes
	SortBy   string // "name" (default), "created", "updated", "company"
	SortDesc bool
	Limit    int
	Offset   int
}
