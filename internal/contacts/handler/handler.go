package handler

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/contacts/contact"
)

// Handler exposes the contacts JSON endpoints.
type Handler struct {
	svc   *contact.Service
	blobs blob.Store
	audit *audit.Service
}

// New builds a Handler.
func New(svc *contact.Service, blobs blob.Store, auditSvc *audit.Service) *Handler {
	return &Handler{svc: svc, blobs: blobs, audit: auditSvc}
}

// RegisterRoutes mounts the authenticated user endpoints on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/contacts", h.listContacts)
	mux.HandleFunc("POST /api/contacts", h.createContact)
	mux.HandleFunc("POST /api/contacts/import", h.importVCards)
	mux.HandleFunc("GET /api/contacts/export", h.exportVCards)

	mux.HandleFunc("GET /api/contacts/{id}", h.getContact)
	mux.HandleFunc("PUT /api/contacts/{id}", h.replaceContact)
	mux.HandleFunc("PATCH /api/contacts/{id}", h.patchContact)
	mux.HandleFunc("DELETE /api/contacts/{id}", h.deleteContact)
	mux.HandleFunc("POST /api/contacts/{id}/avatar", h.uploadAvatar)
	mux.HandleFunc("DELETE /api/contacts/{id}/avatar", h.deleteAvatar)
	mux.HandleFunc("PUT /api/contacts/{id}/groups", h.setContactGroups)

	mux.HandleFunc("GET /api/contacts/groups", h.listGroups)
	mux.HandleFunc("POST /api/contacts/groups", h.createGroup)
	mux.HandleFunc("GET /api/contacts/groups/{id}", h.getGroup)
	mux.HandleFunc("PATCH /api/contacts/groups/{id}", h.updateGroup)
	mux.HandleFunc("DELETE /api/contacts/groups/{id}", h.deleteGroup)
}

// --- DTOs ------------------------------------------------------------------

type emailDTO struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type phoneDTO struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type imDTO struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type urlDTO struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type addressDTO struct {
	Type       string `json:"type,omitempty"`
	Street     string `json:"street,omitempty"`
	Locality   string `json:"locality,omitempty"`
	Region     string `json:"region,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
}

type contactResp struct {
	ID          string       `json:"id"`
	NamePrefix  string       `json:"name_prefix"`
	GivenName   string       `json:"given_name"`
	MiddleName  string       `json:"middle_name"`
	FamilyName  string       `json:"family_name"`
	NameSuffix  string       `json:"name_suffix"`
	DisplayName string       `json:"display_name"`
	Nickname    string       `json:"nickname"`
	Company     string       `json:"company"`
	Title       string       `json:"title"`
	Department  string       `json:"department"`
	Emails      []emailDTO   `json:"emails"`
	Phones      []phoneDTO   `json:"phones"`
	Addresses   []addressDTO `json:"addresses"`
	Ims         []imDTO      `json:"ims"`
	Urls        []urlDTO     `json:"urls"`
	Birthday    string       `json:"birthday,omitempty"`
	Notes       string       `json:"notes"`
	AvatarURL   string       `json:"avatar_url,omitempty"`
	IsFavorite  bool         `json:"is_favorite"`
	GroupIDs    []string     `json:"group_ids"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
}

func toContactResp(c *contact.Contact, groupIDs []string) contactResp {
	out := contactResp{
		ID: c.ID,
		NamePrefix: c.NamePrefix, GivenName: c.GivenName, MiddleName: c.MiddleName,
		FamilyName: c.FamilyName, NameSuffix: c.NameSuffix, DisplayName: c.DisplayName,
		Nickname: c.Nickname, Company: c.Company, Title: c.Title, Department: c.Department,
		Emails: toEmailDTOs(c.Emails), Phones: toPhoneDTOs(c.Phones),
		Addresses: toAddressDTOs(c.Addresses), Ims: toIMDTOs(c.IMs), Urls: toURLDTOs(c.URLs),
		Notes: c.Notes, IsFavorite: c.IsFavorite,
		GroupIDs: groupIDs,
		CreatedAt: c.CreatedAt.UTC().Format(timeRFC3339),
		UpdatedAt: c.UpdatedAt.UTC().Format(timeRFC3339),
	}
	if c.Birthday != nil {
		out.Birthday = c.Birthday.UTC().Format("2006-01-02")
	}
	if c.AvatarPath != "" {
		out.AvatarURL = "/api/files/" + c.AvatarPath
	}
	return out
}

func toEmailDTOs(in []contact.Email) []emailDTO {
	out := make([]emailDTO, 0, len(in))
	for _, e := range in {
		out = append(out, emailDTO{Value: e.Value, Type: string(e.Type)})
	}
	return out
}

func toPhoneDTOs(in []contact.Phone) []phoneDTO {
	out := make([]phoneDTO, 0, len(in))
	for _, p := range in {
		out = append(out, phoneDTO{Value: p.Value, Type: string(p.Type)})
	}
	return out
}

func toAddressDTOs(in []contact.Address) []addressDTO {
	out := make([]addressDTO, 0, len(in))
	for _, a := range in {
		out = append(out, addressDTO{
			Type: string(a.Type), Street: a.Street, Locality: a.Locality,
			Region: a.Region, PostalCode: a.PostalCode, Country: a.Country,
		})
	}
	return out
}

func toIMDTOs(in []contact.IM) []imDTO {
	out := make([]imDTO, 0, len(in))
	for _, m := range in {
		out = append(out, imDTO{Value: m.Value, Type: string(m.Type)})
	}
	return out
}

func toURLDTOs(in []contact.URL) []urlDTO {
	out := make([]urlDTO, 0, len(in))
	for _, u := range in {
		out = append(out, urlDTO{Value: u.Value, Type: string(u.Type)})
	}
	return out
}

type groupResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color,omitempty"`
	Count     int    `json:"count"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toGroupResp(g *contact.Group) groupResp {
	return groupResp{
		ID: g.ID, Name: g.Name, Color: g.Color, Count: g.Count,
		CreatedAt: g.CreatedAt.UTC().Format(timeRFC3339),
		UpdatedAt: g.UpdatedAt.UTC().Format(timeRFC3339),
	}
}

// writeContact fetches the contact's group ids and emits a contactResp.
func (h *Handler) writeContact(w http.ResponseWriter, r *http.Request, status int, c *contact.Contact) {
	groups, err := h.svc.GroupsFor(r.Context(), c.UserID, c.ID)
	if err != nil {
		slog.Warn("contacts: fetch groups for response", "id", c.ID, "error", err)
		groups = nil
	}
	jsonResp(w, status, toContactResp(c, groups))
}

// --- listing ---------------------------------------------------------------

func (h *Handler) listContacts(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := contact.ListFilter{
		UserID:   sess.UserID,
		GroupID:  q.Get("group"),
		Favorite: parseBool(q.Get("favorite")),
		Search:   q.Get("q"),
		SortBy:   q.Get("sort"),
		SortDesc: parseBool(q.Get("order")),
		Limit:    parseInt(q.Get("limit")),
		Offset:   parseInt(q.Get("offset")),
	}
	contacts, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	ids := make([]string, 0, len(contacts))
	for _, c := range contacts {
		ids = append(ids, c.ID)
	}
	groupMap, err := h.svc.GroupsForContacts(r.Context(), sess.UserID, ids)
	if err != nil {
		slog.Warn("contacts: batch fetch groups", "error", err)
		groupMap = map[string][]string{}
	}
	out := make([]contactResp, 0, len(contacts))
	for _, c := range contacts {
		out = append(out, toContactResp(c, groupMap[c.ID]))
	}
	jsonResp(w, http.StatusOK, map[string]any{"contacts": out})
}

// --- create ----------------------------------------------------------------

type contactReq struct {
	NamePrefix  string       `json:"name_prefix"`
	GivenName   string       `json:"given_name"`
	MiddleName  string       `json:"middle_name"`
	FamilyName  string       `json:"family_name"`
	NameSuffix  string       `json:"name_suffix"`
	DisplayName string       `json:"display_name"`
	Nickname    string       `json:"nickname"`
	Company     string       `json:"company"`
	Title       string       `json:"title"`
	Department  string       `json:"department"`
	Emails      []emailDTO   `json:"emails"`
	Phones      []phoneDTO   `json:"phones"`
	Addresses   []addressDTO `json:"addresses"`
	Ims         []imDTO      `json:"ims"`
	Urls        []urlDTO     `json:"urls"`
	Birthday    string       `json:"birthday"`
	Notes       string       `json:"notes"`
	IsFavorite  bool         `json:"is_favorite"`
	GroupIDs    []string     `json:"group_ids"`
}

func (req contactReq) toInput(userID string) contact.CreateContactInput {
	return contact.CreateContactInput{
		UserID: userID,
		NamePrefix: req.NamePrefix, GivenName: req.GivenName, MiddleName: req.MiddleName,
		FamilyName: req.FamilyName, NameSuffix: req.NameSuffix, DisplayName: req.DisplayName,
		Nickname: req.Nickname, Company: req.Company, Title: req.Title, Department: req.Department,
		Emails: fromEmailDTOs(req.Emails), Phones: fromPhoneDTOs(req.Phones),
		Addresses: fromAddressDTOs(req.Addresses), IMs: fromIMDTOs(req.Ims), URLs: fromURLDTOs(req.Urls),
		Birthday: parseDate(req.Birthday), Notes: req.Notes, IsFavorite: req.IsFavorite,
		GroupIDs: req.GroupIDs,
	}
}

func fromEmailDTOs(in []emailDTO) []contact.Email {
	out := make([]contact.Email, 0, len(in))
	for _, e := range in {
		out = append(out, contact.Email{Value: e.Value, Type: contact.Type(e.Type)})
	}
	return out
}

func fromPhoneDTOs(in []phoneDTO) []contact.Phone {
	out := make([]contact.Phone, 0, len(in))
	for _, p := range in {
		out = append(out, contact.Phone{Value: p.Value, Type: contact.Type(p.Type)})
	}
	return out
}

func fromAddressDTOs(in []addressDTO) []contact.Address {
	out := make([]contact.Address, 0, len(in))
	for _, a := range in {
		out = append(out, contact.Address{
			Type: contact.Type(a.Type), Street: a.Street, Locality: a.Locality,
			Region: a.Region, PostalCode: a.PostalCode, Country: a.Country,
		})
	}
	return out
}

func fromIMDTOs(in []imDTO) []contact.IM {
	out := make([]contact.IM, 0, len(in))
	for _, m := range in {
		out = append(out, contact.IM{Value: m.Value, Type: contact.Type(m.Type)})
	}
	return out
}

func fromURLDTOs(in []urlDTO) []contact.URL {
	out := make([]contact.URL, 0, len(in))
	for _, u := range in {
		out = append(out, contact.URL{Value: u.Value, Type: contact.Type(u.Type)})
	}
	return out
}

// parseDate accepts YYYY-MM-DD (and RFC3339); returns nil for empty.
func parseDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, f := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(f, s); err == nil {
			tc := t.UTC().Truncate(time.Second)
			return &tc
		}
	}
	return nil
}

func (h *Handler) createContact(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req contactReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	c, err := h.svc.Create(r.Context(), req.toInput(sess.UserID))
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.contact_created", ActorUserID: sess.UserID,
		TargetType: "contact", TargetID: c.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"name": c.SortName()},
	})
	h.writeContact(w, r, http.StatusCreated, c)
}

// --- single contact --------------------------------------------------------

func (h *Handler) getContact(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	c, err := h.svc.Get(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	h.writeContact(w, r, http.StatusOK, c)
}

func (h *Handler) replaceContact(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req contactReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	c, err := h.svc.Replace(r.Context(), sess.UserID, r.PathValue("id"), req.toInput(sess.UserID))
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.contact_updated", ActorUserID: sess.UserID,
		TargetType: "contact", TargetID: c.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	h.writeContact(w, r, http.StatusOK, c)
}

type patchReq struct {
	NamePrefix  *string      `json:"name_prefix"`
	GivenName   *string      `json:"given_name"`
	MiddleName  *string      `json:"middle_name"`
	FamilyName  *string      `json:"family_name"`
	NameSuffix  *string      `json:"name_suffix"`
	DisplayName *string      `json:"display_name"`
	Nickname    *string      `json:"nickname"`
	Company     *string      `json:"company"`
	Title       *string      `json:"title"`
	Department  *string      `json:"department"`
	Emails      *[]emailDTO  `json:"emails"`
	Phones      *[]phoneDTO  `json:"phones"`
	Addresses   *[]addressDTO `json:"addresses"`
	Ims         *[]imDTO     `json:"ims"`
	Urls        *[]urlDTO    `json:"urls"`
	Birthday    *string      `json:"birthday"`
	Notes       *string      `json:"notes"`
	IsFavorite  *bool        `json:"is_favorite"`
}

func (h *Handler) patchContact(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req patchReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	p := contact.ContactPatch{
		NamePrefix: req.NamePrefix, GivenName: req.GivenName, MiddleName: req.MiddleName,
		FamilyName: req.FamilyName, NameSuffix: req.NameSuffix, DisplayName: req.DisplayName,
		Nickname: req.Nickname, Company: req.Company, Title: req.Title, Department: req.Department,
		Notes: req.Notes, IsFavorite: req.IsFavorite,
	}
	if req.Emails != nil {
		p.Emails = ptrEmails(*req.Emails)
	}
	if req.Phones != nil {
		p.Phones = ptrPhones(*req.Phones)
	}
	if req.Addresses != nil {
		p.Addresses = ptrAddresses(*req.Addresses)
	}
	if req.Ims != nil {
		p.IMs = ptrIMs(*req.Ims)
	}
	if req.Urls != nil {
		p.URLs = ptrURLs(*req.Urls)
	}
	if req.Birthday != nil {
		p.BirthdaySet = true
		p.Birthday = parseDate(*req.Birthday)
	}
	c, err := h.svc.Patch(r.Context(), sess.UserID, r.PathValue("id"), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.contact_updated", ActorUserID: sess.UserID,
		TargetType: "contact", TargetID: c.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	h.writeContact(w, r, http.StatusOK, c)
}

func ptrEmails(in []emailDTO) *[]contact.Email {
	out := fromEmailDTOs(in)
	return &out
}

func ptrPhones(in []phoneDTO) *[]contact.Phone {
	out := fromPhoneDTOs(in)
	return &out
}

func ptrAddresses(in []addressDTO) *[]contact.Address {
	out := fromAddressDTOs(in)
	return &out
}

func ptrIMs(in []imDTO) *[]contact.IM {
	out := fromIMDTOs(in)
	return &out
}

func ptrURLs(in []urlDTO) *[]contact.URL {
	out := fromURLDTOs(in)
	return &out
}

func (h *Handler) deleteContact(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.svc.Delete(r.Context(), sess.UserID, id); err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.contact_deleted", ActorUserID: sess.UserID,
		TargetType: "contact", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- avatar ----------------------------------------------------------------

func (h *Handler) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	// Cap avatar upload to 8 MiB; avatars are small images.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)

	var body io.Reader = r.Body
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			respondErr(w, http.StatusBadRequest, "invalid_request", "could not parse multipart: "+err.Error())
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			respondErr(w, http.StatusBadRequest, "invalid_request", "missing 'file' field")
			return
		}
		defer f.Close()
		body = f
		contentType = r.Header.Get("Content-Type")
	}
	c, err := h.svc.SetAvatar(r.Context(), sess.UserID, r.PathValue("id"), contentType, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.avatar_set", ActorUserID: sess.UserID,
		TargetType: "contact", TargetID: c.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	h.writeContact(w, r, http.StatusOK, c)
}

func (h *Handler) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	c, err := h.svc.ClearAvatar(r.Context(), sess.UserID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.avatar_cleared", ActorUserID: sess.UserID,
		TargetType: "contact", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	h.writeContact(w, r, http.StatusOK, c)
}

// --- group membership ------------------------------------------------------

type setGroupsReq struct {
	GroupIDs []string `json:"group_ids"`
}

func (h *Handler) setContactGroups(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req setGroupsReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.svc.SetGroups(r.Context(), sess.UserID, r.PathValue("id"), req.GroupIDs); err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.groups_set", ActorUserID: sess.UserID,
		TargetType: "contact", TargetID: r.PathValue("id"),
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"count": len(req.GroupIDs)},
	})
	c, err := h.svc.Get(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	h.writeContact(w, r, http.StatusOK, c)
}

// --- groups ----------------------------------------------------------------

type groupReq struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	groups, err := h.svc.ListGroups(r.Context(), sess.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]groupResp, 0, len(groups))
	for _, g := range groups {
		out = append(out, toGroupResp(g))
	}
	jsonResp(w, http.StatusOK, map[string]any{"groups": out})
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req groupReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	g, err := h.svc.CreateGroup(r.Context(), contact.CreateGroupInput{
		UserID: sess.UserID, Name: req.Name, Color: req.Color,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.group_created", ActorUserID: sess.UserID,
		TargetType: "contact_group", TargetID: g.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"name": g.Name},
	})
	jsonResp(w, http.StatusCreated, toGroupResp(g))
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	g, err := h.svc.GetGroup(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, toGroupResp(g))
}

func (h *Handler) updateGroup(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req groupReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	g, err := h.svc.UpdateGroup(r.Context(), sess.UserID, r.PathValue("id"), req.Name, req.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.group_updated", ActorUserID: sess.UserID,
		TargetType: "contact_group", TargetID: g.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	jsonResp(w, http.StatusOK, toGroupResp(g))
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.svc.DeleteGroup(r.Context(), sess.UserID, id); err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.group_deleted", ActorUserID: sess.UserID,
		TargetType: "contact_group", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- vCard import/export ---------------------------------------------------

func (h *Handler) importVCards(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	inputs, err := contact.ParseVCards(r.Body)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", "could not parse vCard: "+err.Error())
		return
	}
	created := 0
	var first contact.Contact
	for _, in := range inputs {
		in.UserID = sess.UserID
		c, err := h.svc.Create(r.Context(), in)
		if err != nil {
			continue
		}
		if created == 0 {
			first = *c
		}
		created++
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.import", ActorUserID: sess.UserID,
		TargetType: "contact", TargetID: first.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"count": created},
	})
	jsonResp(w, http.StatusCreated, map[string]any{
		importedKey: created,
	})
}

const importedKey = "imported"

func (h *Handler) exportVCards(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	contacts, err := h.svc.List(r.Context(), contact.ListFilter{
		UserID: sess.UserID, Limit: 2000, SortBy: r.URL.Query().Get("sort"),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="contacts.vcf"`)
	for _, c := range contacts {
		if err := contact.WriteVCard(w, c); err != nil {
			return
		}
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "contacts.export", ActorUserID: sess.UserID,
		TargetType: "contact",
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"count": len(contacts)},
	})
}
