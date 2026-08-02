package contact

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/blob"
)

// Namespace is the blob-store namespace used for contact avatars.
const Namespace = "contacts"

// Service contains contacts business logic.
type Service struct {
	repo  *Repository
	blobs blob.Store
}

// NewService returns a Service backed by repo. blobs stores avatar bytes
// (may be nil when avatar support is disabled).
func NewService(repo *Repository, blobs blob.Store) *Service {
	return &Service{repo: repo, blobs: blobs}
}

// Create creates a contact. At least one of the name fields, nickname, an
// email, a phone or an organisation must be present so the book never holds an
// entirely blank entry.
func (s *Service) Create(ctx context.Context, in CreateContactInput) (*Contact, error) {
	if !hasAnyIdentity(in) {
		return nil, ErrNameRequired
	}
	in = normalizeCreateInput(in)
	c := &Contact{
		ID:          nextID(),
		UserID:      in.UserID,
		NamePrefix:  in.NamePrefix,
		GivenName:   in.GivenName,
		MiddleName:  in.MiddleName,
		FamilyName:  in.FamilyName,
		NameSuffix:  in.NameSuffix,
		DisplayName: in.DisplayName,
		Nickname:    in.Nickname,
		Company:     in.Company,
		Title:       in.Title,
		Department:  in.Department,
		Emails:      in.Emails,
		Phones:      in.Phones,
		Addresses:   in.Addresses,
		IMs:         in.IMs,
		URLs:        in.URLs,
		Birthday:    in.Birthday,
		Notes:       in.Notes,
		IsFavorite:  in.IsFavorite,
	}
	if err := s.repo.CreateContact(ctx, c); err != nil {
		return nil, err
	}
	if err := s.repo.SetContactGroups(ctx, in.UserID, c.ID, in.GroupIDs); err != nil {
		return nil, err
	}
	return c, nil
}

// Get returns one contact owned by userID.
func (s *Service) Get(ctx context.Context, userID, id string) (*Contact, error) {
	return s.repo.GetContact(ctx, userID, id)
}

// List returns contacts matching the filter.
func (s *Service) List(ctx context.Context, f ListFilter) ([]*Contact, error) {
	return s.repo.ListContacts(ctx, f)
}

// Count returns the number of contacts owned by userID.
func (s *Service) Count(ctx context.Context, userID string) (int, error) {
	return s.repo.CountContacts(ctx, userID)
}

// Replace performs a full update (PUT semantics) of every editable field.
func (s *Service) Replace(ctx context.Context, userID, id string, in UpdateContactInput) (*Contact, error) {
	if !hasAnyIdentity(in) {
		return nil, ErrNameRequired
	}
	in = normalizeCreateInput(in)
	if err := s.repo.UpdateContact(ctx, userID, id, in); err != nil {
		return nil, err
	}
	if err := s.repo.SetContactGroups(ctx, userID, id, in.GroupIDs); err != nil {
		return nil, err
	}
	return s.repo.GetContact(ctx, userID, id)
}

// Patch applies a partial update (PATCH semantics).
func (s *Service) Patch(ctx context.Context, userID, id string, p ContactPatch) (*Contact, error) {
	if err := s.repo.PatchContact(ctx, userID, id, p); err != nil {
		return nil, err
	}
	return s.repo.GetContact(ctx, userID, id)
}

// SetFavorite toggles the favorite flag.
func (s *Service) SetFavorite(ctx context.Context, userID, id string, favorite bool) (*Contact, error) {
	fav := favorite
	return s.Patch(ctx, userID, id, ContactPatch{IsFavorite: &fav})
}

// SetGroups replaces the group memberships for a contact.
func (s *Service) SetGroups(ctx context.Context, userID, id string, groupIDs []string) error {
	return s.repo.SetContactGroups(ctx, userID, id, groupIDs)
}

// GroupsFor returns the group ids a contact belongs to.
func (s *Service) GroupsFor(ctx context.Context, userID, id string) ([]string, error) {
	return s.repo.ContactGroupIDs(ctx, userID, id)
}

// GroupsForContacts returns a contact-id -> group-ids map for batch listings.
func (s *Service) GroupsForContacts(ctx context.Context, userID string, contactIDs []string) (map[string][]string, error) {
	return s.repo.GroupIDsForContacts(ctx, userID, contactIDs)
}

// Delete removes a contact and purges its avatar blob (best-effort).
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	avatar, err := s.repo.DeleteContact(ctx, userID, id)
	if err != nil {
		return err
	}
	if avatar != "" && s.blobs != nil {
		_ = s.blobs.Delete(ctx, avatar)
	}
	return nil
}

// SetAvatar stores the uploaded avatar bytes in the blob store and records the
// path on the contact. The previous avatar (if any) is purged best-effort. The
// caller is responsible for size/MIME guards.
func (s *Service) SetAvatar(ctx context.Context, userID, id, contentType string, body io.Reader) (*Contact, error) {
	if s.blobs == nil {
		return nil, fmt.Errorf("%w: blob store unavailable", ErrInvalidInput)
	}
	c, err := s.repo.GetContact(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	path, err := s.blobs.Save(ctx, Namespace, contentType, body)
	if err != nil {
		return fmt.Errorf("contacts: save avatar: %w", err)
	}
	old := c.AvatarPath
	if err := s.repo.SetAvatar(ctx, userID, id, path); err != nil {
		_ = s.blobs.Delete(ctx, path) // roll back the just-saved blob
		return nil, err
	}
	if old != "" && old != path {
		_ = s.blobs.Delete(ctx, old)
	}
	return s.repo.GetContact(ctx, userID, id)
}

// ClearAvatar removes the avatar reference from a contact and purges its blob
// (best-effort). It is a no-op (returning the contact unchanged) when the
// contact had no avatar.
func (s *Service) ClearAvatar(ctx context.Context, userID, id string) (*Contact, error) {
	c, err := s.repo.GetContact(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if c.AvatarPath == "" {
		return c, nil
	}
	old := c.AvatarPath
	if err := s.repo.SetAvatar(ctx, userID, id, ""); err != nil {
		return nil, err
	}
	if s.blobs != nil {
		_ = s.blobs.Delete(ctx, old)
	}
	return s.repo.GetContact(ctx, userID, id)
}

// --- groups ----------------------------------------------------------------

// CreateGroup creates a group label.
func (s *Service) CreateGroup(ctx context.Context, in CreateGroupInput) (*Group, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrGroupNameRequired
	}
	g := &Group{ID: nextID(), UserID: in.UserID, Name: name, Color: strings.TrimSpace(in.Color)}
	if err := s.repo.CreateGroup(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

// GetGroup returns one group owned by userID.
func (s *Service) GetGroup(ctx context.Context, userID, id string) (*Group, error) {
	return s.repo.GetGroup(ctx, userID, id)
}

// ListGroups returns the groups owned by userID.
func (s *Service) ListGroups(ctx context.Context, userID string) ([]*Group, error) {
	return s.repo.ListGroups(ctx, userID)
}

// UpdateGroup renames / recolors a group.
func (s *Service) UpdateGroup(ctx context.Context, userID, id, name, color string) (*Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrGroupNameRequired
	}
	if err := s.repo.UpdateGroup(ctx, userID, id, name, strings.TrimSpace(color)); err != nil {
		return nil, err
	}
	return s.repo.GetGroup(ctx, userID, id)
}

// DeleteGroup removes a group; membership rows cascade.
func (s *Service) DeleteGroup(ctx context.Context, userID, id string) error {
	return s.repo.DeleteGroup(ctx, userID, id)
}

// --- validation / normalization --------------------------------------------

func hasAnyIdentity(in CreateContactInput) bool {
	if strings.TrimSpace(in.NamePrefix+in.GivenName+in.MiddleName+in.FamilyName+in.NameSuffix+in.DisplayName+in.Nickname) != "" {
		return true
	}
	if strings.TrimSpace(in.Company+in.Title+in.Department) != "" {
		return true
	}
	for _, e := range in.Emails {
		if strings.TrimSpace(e.Value) != "" {
			return true
		}
	}
	for _, p := range in.Phones {
		if strings.TrimSpace(p.Value) != "" {
			return true
		}
	}
	return false
}

// normalizeCreateInput trims strings and drops empty multi-value entries so the
// stored record stays tidy and vCard export stays clean.
func normalizeCreateInput(in CreateContactInput) CreateContactInput {
	in.NamePrefix = strings.TrimSpace(in.NamePrefix)
	in.GivenName = strings.TrimSpace(in.GivenName)
	in.MiddleName = strings.TrimSpace(in.MiddleName)
	in.FamilyName = strings.TrimSpace(in.FamilyName)
	in.NameSuffix = strings.TrimSpace(in.NameSuffix)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Nickname = strings.TrimSpace(in.Nickname)
	in.Company = strings.TrimSpace(in.Company)
	in.Title = strings.TrimSpace(in.Title)
	in.Department = strings.TrimSpace(in.Department)
	in.Notes = strings.TrimSpace(in.Notes)
	in.Emails = cleanEmails(in.Emails)
	in.Phones = cleanPhones(in.Phones)
	in.Addresses = cleanAddresses(in.Addresses)
	in.IMs = cleanIMs(in.IMs)
	in.URLs = cleanURLs(in.URLs)
	if in.Birthday != nil {
		t := in.Birthday.UTC().Truncate(time.Second)
		in.Birthday = &t
	}
	return in
}

func cleanEmails(in []Email) []Email {
	out := make([]Email, 0, len(in))
	for _, e := range in {
		v := strings.TrimSpace(e.Value)
		if v == "" {
			continue
		}
		out = append(out, Email{Value: v, Type: e.Type})
	}
	return out
}

func cleanPhones(in []Phone) []Phone {
	out := make([]Phone, 0, len(in))
	for _, p := range in {
		v := strings.TrimSpace(p.Value)
		if v == "" {
			continue
		}
		out = append(out, Phone{Value: v, Type: p.Type})
	}
	return out
}

func cleanAddresses(in []Address) []Address {
	out := make([]Address, 0, len(in))
	for _, a := range in {
		a.Street = strings.TrimSpace(a.Street)
		a.Locality = strings.TrimSpace(a.Locality)
		a.Region = strings.TrimSpace(a.Region)
		a.PostalCode = strings.TrimSpace(a.PostalCode)
		a.Country = strings.TrimSpace(a.Country)
		if a.Street == "" && a.Locality == "" && a.Region == "" && a.PostalCode == "" && a.Country == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func cleanIMs(in []IM) []IM {
	out := make([]IM, 0, len(in))
	for _, m := range in {
		v := strings.TrimSpace(m.Value)
		if v == "" {
			continue
		}
		out = append(out, IM{Value: v, Type: m.Type})
	}
	return out
}

func cleanURLs(in []URL) []URL {
	out := make([]URL, 0, len(in))
	for _, u := range in {
		v := strings.TrimSpace(u.Value)
		if v == "" {
			continue
		}
		out = append(out, URL{Value: v, Type: u.Type})
	}
	return out
}
