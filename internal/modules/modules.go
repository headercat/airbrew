// Package modules holds the shared registry of all feature modules so the
// server bootstrap, the admin panel listing, and the SPA catalog can all
// reference one source of truth.
//
// Each feature package (mail, drive, admin, ...) exposes:
//
//	type Module struct{ ... }
//	func New(...) *Module
//	func (m *Module) RegisterRoutes(mux *http.ServeMux)
//
// Go's structural typing means we don't need a formal interface here; the
// registry only catalogs metadata.
package modules

// Meta describes a module for listings (admin panel, status endpoint, sidebar).
type Meta struct {
	Key         string
	Name        string
	Description string
	// AdminOnly hides the module from non-admin users in the SPA sidebar and
	// rejects non-admin access on the backend.
	AdminOnly bool
	// System marks a module that cannot be disabled (admin, auth).
	System bool
}

// Catalog is the canonical list of modules in stable display order.
var Catalog = []Meta{
	{
		Key: "admin", Name: "Admin",
		Description: "Workspace administration: modules, users, permissions.",
		AdminOnly: true, System: true,
	},
	{Key: "mail", Name: "Mail", Description: "IMAP/SMTP-style mailboxes with inbound + outbound storage."},
	{Key: "drive", Name: "Drive", Description: "Content-addressed file storage with sharing links."},
	{Key: "contacts", Name: "Contacts", Description: "vCard 4.0 address book with CardDAV sync."},
	{Key: "chat", Name: "Chat", Description: "1:1 and group conversations with real-time delivery."},
	{Key: "ai", Name: "AI Agents", Description: "Tool-using LLM agents with streaming responses."},
	{Key: "workflow", Name: "Workflows", Description: "Trigger/action graph automation engine."},
}

// Find returns the Meta for key, or false if unknown.
func Find(key string) (Meta, bool) {
	for _, m := range Catalog {
		if m.Key == key {
			return m, true
		}
	}
	return Meta{}, false
}

// Keys returns just the module keys in catalog order.
func Keys() []string {
	out := make([]string, len(Catalog))
	for i, m := range Catalog {
		out[i] = m.Key
	}
	return out
}

// DisableableKeys returns the keys of modules that an admin is allowed to
// toggle. System modules (admin) are excluded.
func DisableableKeys() []string {
	var out []string
	for _, m := range Catalog {
		if !m.System {
			out = append(out, m.Key)
		}
	}
	return out
}
