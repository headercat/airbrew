package user

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/auth/password"
	"github.com/headercat/airbrew/internal/id"
)

// Service contains user-facing business logic.
type Service struct {
	repo   *Repository
	policy PasswordPolicyChecker
}

// PasswordPolicyChecker validates a plaintext password against the workspace
// policy. When nil (see SetPasswordPolicyChecker), the service falls back to a
// hard-coded 8-character minimum so the package stays usable in tests and
// bootstrap without a configured policy.
type PasswordPolicyChecker interface {
	ValidatePassword(ctx context.Context, plain string) error
}

// NewService returns a Service backed by repo.
func NewService(repo *Repository) *Service { return &Service{repo: repo} }

// SetPasswordPolicyChecker injects the workspace password policy. It is
// optional; without it the service enforces only the legacy 8-char minimum.
func (s *Service) SetPasswordPolicyChecker(c PasswordPolicyChecker) { s.policy = c }

// ErrInvalidCredentials is returned by Authenticate / ChangePassword on bad
// email or password.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ProfileUpdate replaces all editable profile fields. A nil Birthday clears
// the stored value; the zero time also clears.
type ProfileUpdate struct {
	DisplayName  string
	Description  string
	Birthday     *time.Time
	PhoneNumber  string
	CustomFields map[string]string
}

// Register creates a new user with email + password.
func (s *Service) Register(ctx context.Context, email, plainPassword, displayName string) (*User, error) {
	return s.registerWithRole(ctx, email, plainPassword, displayName, RoleUser)
}

// RegisterAdmin creates a new administrator account. It is used by first-run
// bootstrap and should not be exposed directly to unauthenticated HTTP callers.
func (s *Service) RegisterAdmin(ctx context.Context, email, plainPassword, displayName string) (*User, error) {
	return s.registerWithRole(ctx, email, plainPassword, displayName, RoleAdmin)
}

func (s *Service) registerWithRole(ctx context.Context, email, plainPassword, displayName string, role Role) (*User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("email required")
	}
	if err := s.checkPasswordPolicy(ctx, plainPassword); err != nil {
		return nil, err
	}
	hash, err := password.Hash(plainPassword)
	if err != nil {
		return nil, err
	}
	u := &User{
		ID:            id.New(),
		PublicSubject: id.New(),
		Email:         email,
		Status:        StatusActive,
		Role:          role,
		DisplayName:   strings.TrimSpace(displayName),
		CustomFields:  map[string]string{},
	}
	if err := s.repo.Create(ctx, u, hash); err != nil {
		return nil, err
	}
	return u, nil
}

// Authenticate verifies the email+password and returns the user if valid.
// It deliberately returns ErrInvalidCredentials for any failure mode so
// that callers cannot distinguish "no such user" from "wrong password".
func (s *Service) Authenticate(ctx context.Context, email, plainPassword string) (*User, error) {
	u, err := s.repo.GetByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	hash, err := s.repo.GetPasswordHash(ctx, u.ID)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if err := password.Verify(plainPassword, hash); err != nil {
		return nil, ErrInvalidCredentials
	}
	if u.Status != StatusActive {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// UpdateProfile replaces the editable profile fields of userID with upd.
// A nil Birthday leaves the field untouched; a non-nil pointer to the zero
// time clears it; any other non-nil value sets it (and must not be in the
// future).
func (s *Service) UpdateProfile(ctx context.Context, userID string, upd ProfileUpdate) error {
	displayName := strings.TrimSpace(upd.DisplayName)
	if len(displayName) > 100 {
		return errors.New("display name must be 100 characters or fewer")
	}
	description := strings.TrimSpace(upd.Description)
	if len(description) > 500 {
		return errors.New("description must be 500 characters or fewer")
	}
	phoneNumber := strings.TrimSpace(upd.PhoneNumber)
	if len(phoneNumber) > 32 {
		return errors.New("phone number must be 32 characters or fewer")
	}
	var birthday *time.Time
	if upd.Birthday != nil {
		if upd.Birthday.IsZero() {
			zero := time.Time{}
			birthday = &zero // explicit clear
		} else {
			t := upd.Birthday.UTC()
			if t.After(time.Now()) {
				return errors.New("birthday cannot be in the future")
			}
			birthday = &t
		}
	}
	cf := upd.CustomFields
	if cf == nil {
		cf = map[string]string{}
	}
	if len(cf) > 50 {
		return errors.New("custom fields are limited to 50 keys")
	}
	for k, v := range cf {
		if len(k) > 100 {
			return errors.New("custom field keys must be 100 characters or fewer")
		}
		if len(v) > 500 {
			return errors.New("custom field values must be 500 characters or fewer")
		}
	}
	return s.repo.UpdateProfile(ctx, userID, ProfilePatch{
		DisplayName:  &displayName,
		Description:  &description,
		Birthday:     birthday,
		PhoneNumber:  &phoneNumber,
		CustomFields: &cf,
	})
}

// SetAvatarURL sets the avatar_url field on the user.
func (s *Service) SetAvatarURL(ctx context.Context, userID, avatarURL string) error {
	return s.repo.UpdateProfile(ctx, userID, ProfilePatch{AvatarURL: &avatarURL})
}

// ChangePassword verifies the current password and replaces it with newPassword.
func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if err := s.checkPasswordPolicy(ctx, newPassword); err != nil {
		return err
	}
	u, err := s.repo.GetByID(ctx, userID)
	if err != nil {
		return ErrInvalidCredentials
	}
	hash, err := s.repo.GetPasswordHash(ctx, u.ID)
	if err != nil {
		return ErrInvalidCredentials
	}
	if err := password.Verify(currentPassword, hash); err != nil {
		return ErrInvalidCredentials
	}
	newHash, err := password.Hash(newPassword)
	if err != nil {
		return err
	}
	return s.repo.UpdatePassword(ctx, userID, newHash)
}

// AdminSetPassword replaces a user's password without requiring the current
// one. Used by the admin "reset password" flow.
func (s *Service) AdminSetPassword(ctx context.Context, userID, newPassword string) error {
	if err := s.checkPasswordPolicy(ctx, newPassword); err != nil {
		return err
	}
	hash, err := password.Hash(newPassword)
	if err != nil {
		return err
	}
	return s.repo.UpdatePassword(ctx, userID, hash)
}

// checkPasswordPolicy applies the configured policy when present, otherwise
// falls back to the legacy 8-character minimum.
func (s *Service) checkPasswordPolicy(ctx context.Context, plain string) error {
	if s.policy != nil {
		return s.policy.ValidatePassword(ctx, plain)
	}
	if len(plain) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	return nil
}
