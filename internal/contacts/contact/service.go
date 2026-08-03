package contact

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/blob"
)

// Namespace is the blob-store namespace used for contact avatars.
const Namespace = "contacts"

// Input-size caps protect the store from unbounded client input.
const (
	maxNameLen      = 256     // any single name/label field
	maxNotesLen     = 1 << 14 // 16 KiB
	maxValueListLen = 50      // entries per multi-value field (emails/phones/...)
)

// hexColorRE matches a CSS-style #RRGGBB color (case-insensitive).
var hexColorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

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
	if err := validateInput(in); err != nil {
		return nil, err
	}
	in = normalizeCreateInput(in)
	c := &Contact{
		ID:          nextID(),
		UserID:      in.UserID,
		UID:         in.UID,
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
	if err := s.repo.CreateWithGroups(ctx, c, in.GroupIDs); err != nil {
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

// Export returns contacts with the larger cap used for vCard downloads.
func (s *Service) Export(ctx context.Context, userID, sortBy string) ([]*Contact, error) {
	return s.repo.ListContacts(ctx, ListFilter{
		UserID: userID, Limit: 2000, MaxLimit: 2000, SortBy: sortBy,
	})
}

// Count returns the number of contacts matching the filter (ignoring
// limit/offset/sort), so the UI can render pagination totals.
func (s *Service) Count(ctx context.Context, f ListFilter) (int, error) {
	return s.repo.CountContactsFiltered(ctx, f)
}

// Replace performs a full update (PUT semantics) of every editable field.
func (s *Service) Replace(ctx context.Context, userID, id string, in UpdateContactInput) (*Contact, error) {
	if !hasAnyIdentity(in) {
		return nil, ErrNameRequired
	}
	if err := validateInput(in); err != nil {
		return nil, err
	}
	in = normalizeCreateInput(in)
	if err := s.repo.ReplaceWithGroups(ctx, userID, id, in, in.GroupIDs); err != nil {
		return nil, err
	}
	return s.repo.GetContact(ctx, userID, id)
}

// Patch applies a partial update (PATCH semantics).
func (s *Service) Patch(ctx context.Context, userID, id string, p ContactPatch) (*Contact, error) {
	current, err := s.repo.GetContact(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	p, merged := normalizePatchInput(current, p)
	if !hasAnyIdentity(merged) {
		return nil, ErrNameRequired
	}
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

// ImportSummary records the outcome of a batch vCard import.
type ImportSummary struct {
	Created int
	Updated int
	Failed  int
	// FirstError is the first per-card failure message, surfaced to the user.
	FirstError string
}

// ImportContacts ingests a batch of parsed contacts. A contact with a UID that
// matches an existing row updates that row in place (dedup); others are
// created. GroupNames are resolved to group ids (creating groups as needed).
// Per-card failures are counted but never abort the batch.
func (s *Service) ImportContacts(ctx context.Context, userID string, inputs []CreateContactInput) ImportSummary {
	var sum ImportSummary
	for _, in := range inputs {
		in.UserID = userID
		if len(in.GroupNames) > 0 {
			in.GroupIDs = append(in.GroupIDs, s.resolveGroupNames(ctx, userID, in.GroupNames)...)
		}
		if in.UID != "" {
			if existing, err := s.repo.GetContactByUID(ctx, userID, in.UID); err == nil {
				if _, err := s.Replace(ctx, userID, existing.ID, in); err != nil {
					sum.record(err)
					continue
				}
				sum.Updated++
				continue
			}
		}
		if _, err := s.Create(ctx, in); err != nil {
			sum.record(err)
			continue
		}
		sum.Created++
	}
	return sum
}

func (s *ImportSummary) record(err error) {
	s.Failed++
	if s.FirstError == "" {
		s.FirstError = err.Error()
	}
}

// resolveGroupNames maps group labels to ids, creating any that don't exist.
func (s *Service) resolveGroupNames(ctx context.Context, userID string, names []string) []string {
	groups, err := s.repo.ListGroups(ctx, userID)
	if err != nil {
		return nil
	}
	nameToID := make(map[string]string, len(groups))
	for _, g := range groups {
		nameToID[strings.ToLower(g.Name)] = g.ID
	}
	var ids []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if id, ok := nameToID[strings.ToLower(name)]; ok {
			ids = append(ids, id)
			continue
		}
		g, err := s.CreateGroup(ctx, CreateGroupInput{UserID: userID, Name: name})
		if err != nil {
			continue
		}
		nameToID[strings.ToLower(name)] = g.ID
		ids = append(ids, g.ID)
	}
	return ids
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
		return nil, fmt.Errorf("contacts: save avatar: %w", err)
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
	if len(name) > maxNameLen {
		return nil, fmt.Errorf("%w: group name exceeds %d characters", ErrInvalidInput, maxNameLen)
	}
	color := strings.TrimSpace(in.Color)
	if color != "" && !hexColorRE.MatchString(color) {
		return nil, fmt.Errorf("%w: color must be #RRGGBB", ErrInvalidInput)
	}
	g := &Group{ID: nextID(), UserID: in.UserID, Name: name, Color: color}
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
	if len(name) > maxNameLen {
		return nil, fmt.Errorf("%w: group name exceeds %d characters", ErrInvalidInput, maxNameLen)
	}
	color = strings.TrimSpace(color)
	if color != "" && !hexColorRE.MatchString(color) {
		return nil, fmt.Errorf("%w: color must be #RRGGBB", ErrInvalidInput)
	}
	if err := s.repo.UpdateGroup(ctx, userID, id, name, color); err != nil {
		return nil, err
	}
	return s.repo.GetGroup(ctx, userID, id)
}

// DeleteGroup removes a group; membership rows cascade.
func (s *Service) DeleteGroup(ctx context.Context, userID, id string) error {
	return s.repo.DeleteGroup(ctx, userID, id)
}

// --- validation / normalization --------------------------------------------

// validateInput enforces per-field size and count limits to keep the store
// tidy and reject unbounded client input.
func validateInput(in CreateContactInput) error {
	for _, s := range []string{
		in.NamePrefix, in.GivenName, in.MiddleName, in.FamilyName, in.NameSuffix,
		in.DisplayName, in.Nickname, in.Company, in.Title, in.Department,
	} {
		if len(s) > maxNameLen {
			return fmt.Errorf("%w: a name field exceeds %d characters", ErrInvalidInput, maxNameLen)
		}
	}
	if len(in.Notes) > maxNotesLen {
		return fmt.Errorf("%w: notes exceed %d characters", ErrInvalidInput, maxNotesLen)
	}
	if len(in.Emails) > maxValueListLen || len(in.Phones) > maxValueListLen ||
		len(in.Addresses) > maxValueListLen || len(in.IMs) > maxValueListLen ||
		len(in.URLs) > maxValueListLen {
		return fmt.Errorf("%w: a multi-value field exceeds %d entries", ErrInvalidInput, maxValueListLen)
	}
	const valueMax = maxNameLen * 2
	for _, e := range in.Emails {
		if len(e.Value) > valueMax {
			return fmt.Errorf("%w: an email value is too long", ErrInvalidInput)
		}
	}
	for _, p := range in.Phones {
		if len(p.Value) > valueMax {
			return fmt.Errorf("%w: a phone value is too long", ErrInvalidInput)
		}
	}
	for _, u := range in.URLs {
		if len(u.Value) > valueMax {
			return fmt.Errorf("%w: a URL value is too long", ErrInvalidInput)
		}
	}
	for _, m := range in.IMs {
		if len(m.Value) > valueMax {
			return fmt.Errorf("%w: an IM value is too long", ErrInvalidInput)
		}
	}
	for _, a := range in.Addresses {
		for _, s := range []string{a.Street, a.Locality, a.Region, a.PostalCode, a.Country} {
			if len(s) > valueMax {
				return fmt.Errorf("%w: an address field is too long", ErrInvalidInput)
			}
		}
	}
	return nil
}

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

func normalizePatchInput(current *Contact, p ContactPatch) (ContactPatch, CreateContactInput) {
	merged := CreateContactInput{
		UserID: current.UserID, NamePrefix: current.NamePrefix, GivenName: current.GivenName,
		MiddleName: current.MiddleName, FamilyName: current.FamilyName, NameSuffix: current.NameSuffix,
		DisplayName: current.DisplayName, Nickname: current.Nickname, Company: current.Company,
		Title: current.Title, Department: current.Department, Emails: current.Emails,
		Phones: current.Phones, Addresses: current.Addresses, IMs: current.IMs,
		URLs: current.URLs, Birthday: current.Birthday, Notes: current.Notes,
		IsFavorite: current.IsFavorite,
	}
	if p.NamePrefix != nil {
		*p.NamePrefix = strings.TrimSpace(*p.NamePrefix)
		merged.NamePrefix = *p.NamePrefix
	}
	if p.GivenName != nil {
		*p.GivenName = strings.TrimSpace(*p.GivenName)
		merged.GivenName = *p.GivenName
	}
	if p.MiddleName != nil {
		*p.MiddleName = strings.TrimSpace(*p.MiddleName)
		merged.MiddleName = *p.MiddleName
	}
	if p.FamilyName != nil {
		*p.FamilyName = strings.TrimSpace(*p.FamilyName)
		merged.FamilyName = *p.FamilyName
	}
	if p.NameSuffix != nil {
		*p.NameSuffix = strings.TrimSpace(*p.NameSuffix)
		merged.NameSuffix = *p.NameSuffix
	}
	if p.DisplayName != nil {
		*p.DisplayName = strings.TrimSpace(*p.DisplayName)
		merged.DisplayName = *p.DisplayName
	}
	if p.Nickname != nil {
		*p.Nickname = strings.TrimSpace(*p.Nickname)
		merged.Nickname = *p.Nickname
	}
	if p.Company != nil {
		*p.Company = strings.TrimSpace(*p.Company)
		merged.Company = *p.Company
	}
	if p.Title != nil {
		*p.Title = strings.TrimSpace(*p.Title)
		merged.Title = *p.Title
	}
	if p.Department != nil {
		*p.Department = strings.TrimSpace(*p.Department)
		merged.Department = *p.Department
	}
	if p.Emails != nil {
		cleaned := cleanEmails(*p.Emails)
		p.Emails = &cleaned
		merged.Emails = cleaned
	}
	if p.Phones != nil {
		cleaned := cleanPhones(*p.Phones)
		p.Phones = &cleaned
		merged.Phones = cleaned
	}
	if p.Addresses != nil {
		cleaned := cleanAddresses(*p.Addresses)
		p.Addresses = &cleaned
		merged.Addresses = cleaned
	}
	if p.IMs != nil {
		cleaned := cleanIMs(*p.IMs)
		p.IMs = &cleaned
		merged.IMs = cleaned
	}
	if p.URLs != nil {
		cleaned := cleanURLs(*p.URLs)
		p.URLs = &cleaned
		merged.URLs = cleaned
	}
	if p.BirthdaySet {
		if p.Birthday != nil {
			t := p.Birthday.UTC().Truncate(time.Second)
			p.Birthday = &t
		}
		merged.Birthday = p.Birthday
	}
	if p.Notes != nil {
		*p.Notes = strings.TrimSpace(*p.Notes)
		merged.Notes = *p.Notes
	}
	if p.IsFavorite != nil {
		merged.IsFavorite = *p.IsFavorite
	}
	return p, merged
}
