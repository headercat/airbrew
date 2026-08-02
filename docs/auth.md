# Auth module

Airbrew ships an OAuth 2.1 / OpenID Connect provider. This document tracks the
implementation plan, current status, and policy decisions.

## MVP scope (OAuth 2.1)

- Authorization Code flow with **mandatory PKCE** (`S256` only)
- OIDC discovery (`/.well-known/openid-configuration`) and JWKS (`/oauth/jwks`)
- JWT access tokens (5–15 min) and ID tokens
- Opaque refresh tokens with rotation and family-based reuse detection
- Public and confidential clients
- Exact-match `redirect_uri` enforcement
- Browser login sessions (cookie-based, server-stored)
- Per-user / per-client consent
- RP-Initiated Logout

## Out of scope (for now)

SAML, LDAP, SCIM, device flow, client-credentials grant, dynamic client
registration, social login, WebAuthn/passkeys, multi-tenant issuer.

## Milestone roadmap

| # | Milestone                                            | Status |
| - | ---------------------------------------------------- | ------ |
| 1 | User/password/session model + register/login/logout  | ✅ Done |
| 2 | OAuth client CRUD + admin endpoints                  | ✅ Done |
| 3 | `/oauth/authorize` request validation                | ⏳     |
| 4 | Login flow + consent screen                          | ⏳     |
| 5 | Authorization code issuance + `/oauth/token`         | ⏳     |
| 6 | JWT access/ID token issuance (`RS256` / `ES256`)     | ⏳     |
| 7 | JWK rotation + OIDC discovery + JWKS endpoint        | ⏳     |
| 8 | Refresh token rotation + reuse-detection revocation  | ⏳     |
| 9 | `/oauth/revoke` + `/oauth/introspect`                | ⏳     |
| 10| Audit log writes + rate limits                       | ⏳     |
| 11| End-to-end test suite                                | ⏳     |

The data model for milestones 2–9 is **already in the schema**
(`internal/db/migrations/0001_initial.sql`) so no migration is needed to
implement them.

## Token policies

| Token              | TTL             | Storage                                          |
| ------------------ | --------------- | ------------------------------------------------ |
| Authorization code | 5 minutes       | SHA-256 hash, single-use, PKCE-bound, session-bound |
| Access token (JWT) | 5–15 minutes    | Stateless JWT signed with active `oauth_signing_keys` row |
| ID token (JWT)     | 5–15 minutes    | Stateless JWT, audience = `client_id`            |
| Refresh token      | 14–30 days      | Opaque, SHA-256 hash, rotation with family tree  |
| Session cookie     | 14 days default | Opaque, SHA-256 hash, server-revocable           |

## Identifier policy

- All PKs: `internal/id` (21-char nanoid-style).
- Opaque tokens: 32 random bytes, base64url-encoded, stored as SHA-256 hash.
- Client secrets: 32 random bytes, base64url-encoded, stored as SHA-256 hash.
- Users: `id` (internal FK) and a separate `public_subject` used as the OIDC
  `sub`. The two are unrelated so a user PK migration would not invalidate
  issued tokens.

## OAuth client registration policies

- `client_id` values are generated server-side with the `airbrew_` prefix.
- Confidential client secrets are shown once, stored only as SHA-256 hashes, and
  can be rotated from the admin UI.
- Confidential clients may use `client_secret_basic` or `client_secret_post`;
  public clients always use `none`.
- Token endpoint helpers reject inactive clients, wrong client auth methods,
  bad secrets, and duplicate Basic/body credentials.
- Redirect URIs are exact-match values with no fragments. HTTPS is accepted for
  web clients, HTTP is accepted only for loopback hosts, and public clients may
  use reverse-domain private-use schemes such as
  `com.example.app:/oauth/callback`.
- Scope values are normalized, deduplicated, sorted, capped per client, and
  validated against OAuth scope-token syntax.

## Endpoint inventory (target)

```
# Public
GET  /.well-known/openid-configuration       M7
GET  /oauth/jwks                             M7
GET  /oauth/authorize                        M3, M4, M5
POST /oauth/token                            M5, M6, M8
POST /oauth/revoke                           M9
POST /oauth/introspect                       M9
GET  /oauth/logout                           (RP-Initiated Logout)

# Browser
POST /api/auth/register                      M1 ✅
POST /api/auth/login                         M1 ✅
POST /api/auth/logout                        M1 ✅
GET  /api/auth/me                            M1 ✅

# Resource server
GET  /api/users/me                           M1 ✅ (via /api/auth/me)

# Admin
POST   /api/admin/oauth/clients              M2
GET    /api/admin/oauth/clients              M2
GET    /api/admin/oauth/clients/:id          M2
PATCH  /api/admin/oauth/clients/:id          M2
DELETE /api/admin/oauth/clients/:id          M2
GET    /api/admin/audit-logs                 M10
```

## Decisions still open

- **Signing algorithm.** Default to `RS256`. `ES256` is preferable for token
  size; will decide in milestone 6.
- **Encrypted private JWK at rest.** The schema reserves
  `oauth_signing_keys.private_jwk_encrypted` — the encryption scheme (AES-GCM
  keyed by `AIRBREW_SESSION_SECRET` or a separate `AIRBREW_MASTER_KEY`) will be
  decided when milestone 7 lands.
- **Refresh-token reuse detection response.** Either (a) revoke the entire
  family on first reuse or (b) revoke + alert. Will decide in milestone 8.
- **Audit-log structure.** `metadata` is free-form TEXT today. We may move it
  to JSON with validation when milestone 10 starts.
