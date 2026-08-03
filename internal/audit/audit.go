// Package audit provides an append-only audit log service that records
// significant state-changing actions across all modules.
//
// Every entry captures who did what, to which target, from where, and with
// what additional context. The log is the single source of truth for admin
// visibility and compliance.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// Entry describes one audit-worthy event.
type Entry struct {
	EventType     string // e.g. "user.created", "module.disabled"
	ActorUserID   string // internal user ID; empty for system
	ActorClientID string // OAuth client ID; empty for browser
	TargetType    string // "user", "module", "session", "oauth_client"
	TargetID      string // ID of the target resource
	IPAddress     string
	UserAgent     string
	Metadata      map[string]any // additional structured context
}

// Service writes entries to the audit_logs table.
type Service struct {
	db *sql.DB
}

// NewService returns a Service bound to db.
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Log persists one entry. It never returns an error that would fail the
// caller's transaction — audit logging is best-effort by design so that a
// logging failure cannot block a user-facing operation.
func (s *Service) Log(ctx context.Context, e Entry) {
	if s == nil {
		return
	}
	meta := "{}"
	if e.Metadata != nil {
		if b, err := json.Marshal(e.Metadata); err == nil {
			meta = string(b)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_logs
		  (id, actor_user_id, actor_client_id, event_type, target_type, target_id,
		   ip_address, user_agent, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		id.New(),
		nullable(e.ActorUserID),
		nullable(e.ActorClientID),
		e.EventType,
		nullable(e.TargetType),
		nullable(e.TargetID),
		nullable(e.IPAddress),
		nullable(e.UserAgent),
		meta,
		now,
	)
	// Resolve the actor's email once and reuse it for both the human message
	// and the structured log attr. Previously this fired two SELECTs per log.
	actor := actorEmail(s.db, ctx, e.ActorUserID, e.ActorClientID)
	target := targetLabel(e.TargetType, e.TargetID)
	slog.Default().Info(humanMessage(actor, target, e),
		"event", e.EventType,
		"actor", actor,
		"target", target,
		"ip", e.IPAddress,
	)
}

// ListFilter controls which entries are returned.
type ListFilter struct {
	EventType  string
	ActorID    string
	TargetType string
	TargetID   string
	From       time.Time
	To         time.Time
	Limit      int
	Offset     int
}

// LogEntry is a row read back from audit_logs.
type LogEntry struct {
	ID            string    `json:"id"`
	ActorUserID   string    `json:"actor_user_id"`
	ActorClientID string    `json:"actor_client_id"`
	ActorEmail    string    `json:"actor_email,omitempty"`
	EventType     string    `json:"event_type"`
	TargetType    string    `json:"target_type"`
	TargetID      string    `json:"target_id"`
	IPAddress     string    `json:"ip_address"`
	UserAgent     string    `json:"user_agent"`
	Metadata      string    `json:"metadata"`
	CreatedAt     time.Time `json:"created_at"`
}

// List returns audit entries matching the filter, newest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]LogEntry, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := "WHERE 1=1"
	args := []any{}
	if f.EventType != "" {
		where += " AND event_type = ?"
		args = append(args, f.EventType)
	}
	if f.ActorID != "" {
		where += ` AND (
			actor_user_id = ?
			OR actor_client_id = ?
			OR actor_user_id IN (
				SELECT id FROM users WHERE email LIKE ? COLLATE NOCASE
			)
		)`
		args = append(args, f.ActorID, f.ActorID, "%"+f.ActorID+"%")
	}
	if f.TargetType != "" {
		where += " AND target_type = ?"
		args = append(args, f.TargetType)
	}
	if f.TargetID != "" {
		where += " AND target_id = ?"
		args = append(args, f.TargetID)
	}
	if !f.From.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where += " AND created_at <= ?"
		args = append(args, f.To)
	}

	var total int
	countErr := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM audit_logs "+where, args...,
	).Scan(&total)
	if countErr != nil {
		return nil, 0, countErr
	}

	query := `
		SELECT a.id, COALESCE(a.actor_user_id,''), COALESCE(a.actor_client_id,''),
		       COALESCE(u.email,''), a.event_type,
		       COALESCE(a.target_type,''), COALESCE(a.target_id,''),
		       COALESCE(a.ip_address,''), COALESCE(a.user_agent,''),
		       COALESCE(a.metadata,'{}'), a.created_at
		FROM audit_logs a
		LEFT JOIN users u ON u.id = a.actor_user_id
	` + where + `
	ORDER BY a.created_at DESC
	LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []LogEntry
	for rows.Next() {
		var le LogEntry
		if err := rows.Scan(
			&le.ID, &le.ActorUserID, &le.ActorClientID,
			&le.ActorEmail, &le.EventType,
			&le.TargetType, &le.TargetID,
			&le.IPAddress, &le.UserAgent,
			&le.Metadata, &le.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		out = append(out, le)
	}
	return out, total, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func humanMessage(actor, target string, e Entry) string {
	switch e.EventType {
	case "admin.bootstrap":
		return "Bootstrap admin account was created"
	case "user.created":
		return fmt.Sprintf("%s created user %s", actor, metadataOrTarget(e, "email", target))
	case "user.updated":
		return fmt.Sprintf("%s updated user %s", actor, target)
	case "user.role_changed":
		return fmt.Sprintf("%s changed %s role from %s to %s", actor, target, metadataValue(e, "from"), metadataValue(e, "to"))
	case "user.status_changed", "user.suspended", "user.deleted":
		return fmt.Sprintf("%s changed %s status from %s to %s", actor, target, metadataValue(e, "from"), metadataValue(e, "to"))
	case "user.password_reset":
		return fmt.Sprintf("%s reset password for %s", actor, target)
	case "user.password_changed":
		return fmt.Sprintf("%s changed their password", actor)
	case "session.login":
		return fmt.Sprintf("%s signed in", actor)
	case "session.logout":
		return fmt.Sprintf("%s signed out", actor)
	case "session.revoked":
		return fmt.Sprintf("%s revoked session %s", actor, e.TargetID)
	case "module.enabled":
		return fmt.Sprintf("%s enabled module %s", actor, e.TargetID)
	case "module.disabled":
		return fmt.Sprintf("%s disabled module %s", actor, e.TargetID)
	case "branding.updated":
		return fmt.Sprintf("%s updated workspace branding", actor)
	case "security.password_policy_changed":
		return fmt.Sprintf("%s changed the password policy (min length %s)", actor, metadataValue(e, "min_length"))
	case "security.ip_allowlist_changed":
		return fmt.Sprintf("%s updated the IP allowlist (%s entries, enabled=%s)", actor, metadataValue(e, "count"), metadataValue(e, "enabled"))
	case "oauth.client_created":
		return fmt.Sprintf("%s created OAuth client %s", actor, metadataOrTarget(e, "name", target))
	case "oauth.client_updated":
		return fmt.Sprintf("%s updated OAuth client %s", actor, metadataOrTarget(e, "name", target))
	case "oauth.client_deleted":
		return fmt.Sprintf("%s deleted OAuth client %s", actor, metadataOrTarget(e, "name", target))
	case "oauth.client_secret_rotated":
		return fmt.Sprintf("%s rotated the secret for OAuth client %s", actor, target)
	case "profile.updated":
		return fmt.Sprintf("%s updated their profile", actor)
	case "avatar.uploaded":
		return fmt.Sprintf("%s uploaded an avatar", actor)
	case "drive.folder_created":
		return fmt.Sprintf("%s created folder %s", actor, metadataOrTarget(e, "name", target))
	case "drive.file_uploaded":
		return fmt.Sprintf("%s uploaded file %s", actor, metadataOrTarget(e, "name", target))
	case "drive.file_renamed":
		return fmt.Sprintf("%s renamed %s", actor, target)
	case "drive.file_moved":
		return fmt.Sprintf("%s moved %s", actor, target)
	case "drive.file_starred":
		return fmt.Sprintf("%s toggled star on %s", actor, target)
	case "drive.file_copied":
		return fmt.Sprintf("%s copied %s", actor, target)
	case "drive.file_restored":
		return fmt.Sprintf("%s restored %s from trash", actor, target)
	case "drive.file_trashed":
		return fmt.Sprintf("%s moved %s to trash", actor, target)
	case "drive.file_deleted":
		return fmt.Sprintf("%s permanently deleted %s", actor, target)
	case "drive.trash_emptied":
		return fmt.Sprintf("%s emptied their trash", actor)
	case "drive.share_created":
		return fmt.Sprintf("%s created a share link for %s", actor, target)
	case "drive.share_revoked":
		return fmt.Sprintf("%s revoked share %s", actor, target)
	case "drive.config_updated":
		return fmt.Sprintf("%s updated drive storage limits", actor)
	case "contacts.contact_created":
		return fmt.Sprintf("%s created contact %s", actor, metadataOrTarget(e, "name", target))
	case "contacts.contact_updated":
		return fmt.Sprintf("%s updated contact %s", actor, target)
	case "contacts.contact_deleted":
		return fmt.Sprintf("%s deleted contact %s", actor, target)
	case "contacts.avatar_set":
		return fmt.Sprintf("%s set an avatar for %s", actor, target)
	case "contacts.avatar_cleared":
		return fmt.Sprintf("%s removed the avatar for %s", actor, target)
	case "contacts.groups_set":
		return fmt.Sprintf("%s updated group membership for %s", actor, target)
	case "contacts.group_created":
		return fmt.Sprintf("%s created group %s", actor, metadataOrTarget(e, "name", target))
	case "contacts.group_updated":
		return fmt.Sprintf("%s updated group %s", actor, target)
	case "contacts.group_deleted":
		return fmt.Sprintf("%s deleted group %s", actor, target)
	case "contacts.import":
		return fmt.Sprintf("%s imported %s contacts", actor, metadataValue(e, "count"))
	case "contacts.export":
		return fmt.Sprintf("%s exported their contacts", actor)
	case "mail.provider_upserted":
		return fmt.Sprintf("%s configured mail provider %s (%s)", actor, metadataValue(e, "driver"), metadataValue(e, "direction"))
	case "mail.provider_deleted":
		return fmt.Sprintf("%s removed mail provider %s", actor, target)
	case "vault.setup":
		return fmt.Sprintf("%s initialized their password vault", actor)
	case "vault.keys_rotated":
		return fmt.Sprintf("%s rotated their vault master password", actor)
	case "vault.item_created":
		return fmt.Sprintf("%s created a vault item", actor)
	case "vault.item_updated":
		return fmt.Sprintf("%s updated vault item %s", actor, e.TargetID)
	case "vault.item_deleted":
		return fmt.Sprintf("%s deleted vault item %s", actor, e.TargetID)
	case "vault.folder_created":
		return fmt.Sprintf("%s created a vault folder", actor)
	case "vault.folder_updated":
		return fmt.Sprintf("%s updated vault folder %s", actor, e.TargetID)
	case "vault.folder_deleted":
		return fmt.Sprintf("%s deleted vault folder %s", actor, e.TargetID)
	case "vault.export":
		return fmt.Sprintf("%s exported their vault", actor)
	case "vault.import":
		return fmt.Sprintf("%s imported %s entries into their vault", actor, metadataValue(e, "count"))
	default:
		if target != "" {
			return fmt.Sprintf("%s performed %s on %s", actor, e.EventType, target)
		}
		return fmt.Sprintf("%s performed %s", actor, e.EventType)
	}
}

// actorEmail resolves a single human-readable label for the actor. It performs
// at most one DB lookup per call (the previous implementation queried once per
// human message AND once per log attr).
func actorEmail(db *sql.DB, ctx context.Context, userID, clientID string) string {
	if userID != "" && db != nil {
		var email string
		if err := db.QueryRowContext(ctx, "SELECT email FROM users WHERE id = ?", userID).Scan(&email); err == nil && email != "" {
			return email
		}
		return "user " + userID
	}
	if clientID != "" {
		return "OAuth client " + clientID
	}
	return "system"
}

func targetLabel(targetType, targetID string) string {
	if targetType == "" && targetID == "" {
		return ""
	}
	if targetType == "" {
		return targetID
	}
	if targetID == "" {
		return targetType
	}
	return strings.ReplaceAll(targetType, "_", " ") + " " + targetID
}

func metadataOrTarget(e Entry, key, fallback string) string {
	if v := metadataValue(e, key); v != "" {
		return v
	}
	return fallback
}

func metadataValue(e Entry, key string) string {
	if e.Metadata == nil {
		return ""
	}
	v, ok := e.Metadata[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
