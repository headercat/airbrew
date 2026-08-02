package oauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// ClientRepository persists OAuth clients and their redirect URIs.
type ClientRepository struct {
	db *sql.DB
}

// NewClientRepository returns a repository bound to db.
func NewClientRepository(db *sql.DB) *ClientRepository { return &ClientRepository{db: db} }

const clientColumns = `id, client_id, name, client_type, COALESCE(client_secret_hash, ''),
	token_endpoint_auth_method, allowed_scopes, is_first_party, require_consent,
	is_active, created_at, updated_at`

// Create inserts the client and its redirect URIs in one transaction.
func (r *ClientRepository) Create(ctx context.Context, c *Client) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("oauth client: begin tx: %w", err)
	}
	defer tx.Rollback()

	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM oauth_clients WHERE client_id = ?", c.ClientID).Scan(&existing)
	if err == nil {
		return ErrClientIDTaken
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("oauth client: check client_id: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	c.CreatedAt = now
	c.UpdatedAt = now
	c.IsActive = true
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oauth_clients
		  (id, client_id, name, client_type, client_secret_hash,
		   token_endpoint_auth_method, allowed_scopes, is_first_party,
		   require_consent, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
	`,
		c.ID, c.ClientID, c.Name, string(c.ClientType), nullable(c.ClientSecretHash),
		string(c.TokenEndpointAuthMethod), strings.Join(c.AllowedScopes, " "),
		c.IsFirstParty, c.RequireConsent, now, now,
	); err != nil {
		return fmt.Errorf("oauth client: insert: %w", err)
	}
	if err := replaceURIs(ctx, tx, "oauth_client_redirect_uris", c.ID, c.RedirectURIs); err != nil {
		return err
	}
	if err := replaceURIs(ctx, tx, "oauth_client_post_logout_redirect_uris", c.ID, c.PostLogoutRedirectURIs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("oauth client: commit: %w", err)
	}
	return nil
}

// List returns active, non-deleted clients newest first.
func (r *ClientRepository) List(ctx context.Context, limit, offset int) ([]*Client, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+clientColumns+` FROM oauth_clients
		 WHERE deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, c := range out {
		if err := r.loadURIs(ctx, c); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Count returns the number of non-deleted clients.
func (r *ClientRepository) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM oauth_clients WHERE deleted_at IS NULL").Scan(&n)
	return n, err
}

// GetByID returns one non-deleted client by internal ID.
func (r *ClientRepository) GetByID(ctx context.Context, id string) (*Client, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+clientColumns+" FROM oauth_clients WHERE id = ? AND deleted_at IS NULL", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrClientNotFound
	}
	c, err := scanClient(rows)
	if err != nil {
		return nil, err
	}
	if rows.Next() {
		return nil, fmt.Errorf("oauth client: multiple rows for id %q", id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := r.loadURIs(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// GetByClientID returns one active, non-deleted client by its public OAuth
// client_id. The authorize / token endpoints resolve clients this way (the
// protocol never exposes the internal ID).
func (r *ClientRepository) GetByClientID(ctx context.Context, clientID string) (*Client, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+clientColumns+" FROM oauth_clients WHERE client_id = ? AND is_active = 1 AND deleted_at IS NULL", clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrClientNotFound
	}
	c, err := scanClient(rows)
	if err != nil {
		return nil, err
	}
	if rows.Next() {
		return nil, fmt.Errorf("oauth client: multiple rows for client_id %q", clientID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := r.loadURIs(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateSecret replaces a confidential client's secret hash and bumps
// updated_at. It is used by ClientService.RotateSecret.
func (r *ClientRepository) UpdateSecret(ctx context.Context, id, secretHash string) error {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE oauth_clients
		   SET client_secret_hash = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL
	`, nullable(secretHash), now, id)
	if err != nil {
		return fmt.Errorf("oauth client: update secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	return nil
}

// RevokeIssuedCredentials invalidates outstanding authorization codes and
// refresh tokens for one client. Access tokens are short-lived and stateless in
// the planned OAuth flow; refresh-token revocation prevents further renewal.
func (r *ClientRepository) RevokeIssuedCredentials(ctx context.Context, id string) (IssuedCredentialRevocation, error) {
	now := time.Now().UTC().Truncate(time.Second)
	codeRes, err := r.db.ExecContext(ctx, `
		UPDATE oauth_authorization_codes
		   SET consumed_at = ?
		 WHERE oauth_client_id = ?
		   AND consumed_at IS NULL
		   AND expires_at > ?
	`, now, id, now)
	if err != nil {
		return IssuedCredentialRevocation{}, fmt.Errorf("oauth client: revoke authorization codes: %w", err)
	}
	tokenRes, err := r.db.ExecContext(ctx, `
		UPDATE oauth_refresh_tokens
		   SET revoked_at = ?
		 WHERE oauth_client_id = ?
		   AND revoked_at IS NULL
		   AND expires_at > ?
	`, now, id, now)
	if err != nil {
		return IssuedCredentialRevocation{}, fmt.Errorf("oauth client: revoke refresh tokens: %w", err)
	}
	codes, _ := codeRes.RowsAffected()
	tokens, _ := tokenRes.RowsAffected()
	return IssuedCredentialRevocation{AuthorizationCodes: codes, RefreshTokens: tokens}, nil
}

// Update applies editable fields and replaces redirect URI sets.
func (r *ClientRepository) Update(ctx context.Context, c *Client) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("oauth client: begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Truncate(time.Second)
	res, err := tx.ExecContext(ctx, `
		UPDATE oauth_clients
		   SET name = ?, token_endpoint_auth_method = ?, allowed_scopes = ?,
		       is_first_party = ?, require_consent = ?, is_active = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL
	`,
		c.Name, string(c.TokenEndpointAuthMethod), strings.Join(c.AllowedScopes, " "),
		c.IsFirstParty, c.RequireConsent, c.IsActive, now, c.ID,
	)
	if err != nil {
		return fmt.Errorf("oauth client: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	if err := replaceURIs(ctx, tx, "oauth_client_redirect_uris", c.ID, c.RedirectURIs); err != nil {
		return err
	}
	if err := replaceURIs(ctx, tx, "oauth_client_post_logout_redirect_uris", c.ID, c.PostLogoutRedirectURIs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("oauth client: commit: %w", err)
	}
	return nil
}

// Delete soft-deletes a client.
func (r *ClientRepository) Delete(ctx context.Context, id string) error {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx,
		"UPDATE oauth_clients SET deleted_at = ?, is_active = 0, updated_at = ? WHERE id = ? AND deleted_at IS NULL",
		now, now, id,
	)
	if err != nil {
		return fmt.Errorf("oauth client: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	return nil
}

func (r *ClientRepository) loadURIs(ctx context.Context, c *Client) error {
	redirects, err := listURIs(ctx, r.db, "oauth_client_redirect_uris", c.ID)
	if err != nil {
		return err
	}
	postLogout, err := listURIs(ctx, r.db, "oauth_client_post_logout_redirect_uris", c.ID)
	if err != nil {
		return err
	}
	c.RedirectURIs = redirects
	c.PostLogoutRedirectURIs = postLogout
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanClient(row rowScanner) (*Client, error) {
	var c Client
	var clientType, authMethod, scopes string
	if err := row.Scan(
		&c.ID, &c.ClientID, &c.Name, &clientType, &c.ClientSecretHash,
		&authMethod, &scopes, &c.IsFirstParty, &c.RequireConsent,
		&c.IsActive, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	c.ClientType = ClientType(clientType)
	c.TokenEndpointAuthMethod = TokenEndpointAuthMethod(authMethod)
	c.AllowedScopes = strings.Fields(scopes)
	return &c, nil
}

type execQueryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func replaceURIs(ctx context.Context, tx execQueryer, table, clientID string, uris []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE oauth_client_id = ?", clientID); err != nil {
		return fmt.Errorf("oauth client: clear %s: %w", table, err)
	}
	for _, uri := range uris {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO "+table+" (id, oauth_client_id, redirect_uri) VALUES (?, ?, ?)",
			id.New(), clientID, uri,
		); err != nil {
			return fmt.Errorf("oauth client: insert %s: %w", table, err)
		}
	}
	return nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listURIs(ctx context.Context, q queryer, table, clientID string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT redirect_uri FROM "+table+" WHERE oauth_client_id = ? ORDER BY created_at ASC, redirect_uri ASC",
		clientID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uri string
		if err := rows.Scan(&uri); err != nil {
			return nil, err
		}
		out = append(out, uri)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
