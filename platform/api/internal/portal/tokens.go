package portal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"platform/api/internal/adminz"
	"platform/api/internal/apitoken"
)

// API tokens (contract block 6, "Non-browser clients"). Personal tokens are created by their owner from a
// browser session; robot tokens come with the robots (a later part) and share the repository and the revocation.

// Token scopes (mirror the api_tokens.scope CHECK).
const (
	TokenScopeRead  = "read"
	TokenScopeAdmin = "admin"
)

// Token limits and defaults.
const (
	// MaxLiveTokens is how many unexpired, unrevoked tokens one owner may hold.
	MaxLiveTokens = 25
	// maxTokenName is api_tokens_name_check, in characters.
	maxTokenName = 64
	// DefaultTokenMaxTTLPersonal and DefaultTokenMaxTTLRobot are the caps when the configuration gives none
	// (PORTAL_TOKEN_MAX_TTL_PERSONAL, PORTAL_TOKEN_MAX_TTL_ROBOT).
	DefaultTokenMaxTTLPersonal = 720 * time.Hour
	DefaultTokenMaxTTLRobot    = 2160 * time.Hour
)

// Target type and audit actions of the tokens. The audit names are the ones of the contract's Audit section;
// the authorization actions (adminz.Token*Own, TokenRevokeAny) differ because they describe who may ask.
const (
	TargetToken                    = "token"
	AuditTokenCreate adminz.Action = "token.create"
	AuditTokenRevoke adminz.Action = "token.revoke"
)

// Validation codes specific to tokens.
const (
	CodeNotAllowed = "not_allowed"
	CodeTooLarge   = "too_large"
)

// ErrTokenLimit is the conflict of creating a token when the owner already holds MaxLiveTokens live ones.
var ErrTokenLimit = &Conflict{Detail: "the owner already has the maximum number of live tokens"}

// APIToken is a token as the API shows it: never the secret and never the hash.
type APIToken struct {
	ID         string
	UserID     string // owner, human or robot
	Name       string
	Scope      string
	CreatedBy  string // empty when the creator no longer exists
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// NewToken is the request to create a personal token. ExpiresIn nil means the cap.
type NewToken struct {
	Name      string
	Scope     string
	ExpiresIn *time.Duration
}

// CreatedToken is the answer of a creation: the one time the secret exists outside the client.
type CreatedToken struct {
	APIToken
	Secret string
}

// TokenRow is a token ready to be inserted. TTL counts from the database clock, so expires_at is always
// after created_at.
type TokenRow struct {
	OwnerID   string
	CreatedBy string
	Name      string
	Scope     string
	Hash      []byte
	TTL       time.Duration
}

// TokenRepo is the storage of API tokens. It never reads or returns token_hash (the portal role cannot).
type TokenRepo interface {
	// CountLive counts the owner's unexpired, unrevoked tokens.
	CountLive(ctx context.Context, ownerID string) (int, error)
	// InsertToken stores the token and returns it as stored.
	InsertToken(ctx context.Context, row TokenRow) (APIToken, error)
	// GetToken returns ErrNotFound for an unknown id.
	GetToken(ctx context.Context, id string) (APIToken, error)
	// TokensOf returns the owner's tokens, live and recently dead, newest first (keyset on the id, which is
	// time-ordered).
	TokensOf(ctx context.Context, ownerID string, q PageQuery) ([]APIToken, error)
	// RevokeToken sets revoked_at on a token that has none and reports whether it changed anything.
	RevokeToken(ctx context.Context, id string) (bool, error)
	// RevokeByHash revokes the live token whose secret hashes to hash and returns its id; "" when none matched
	// (unknown, expired or already revoked). The repository cannot read hashes: it calls a database function.
	RevokeByHash(ctx context.Context, hash []byte) (string, error)
}

// tokenDoc is the audit document of a token: name, scope and expiry, never the hash.
type tokenDoc struct {
	OwnerID   string    `json:"owner_id"`
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expires_at"`
}

func docOfToken(t APIToken) tokenDoc {
	return tokenDoc{OwnerID: t.UserID, Name: t.Name, Scope: t.Scope, ExpiresAt: t.ExpiresAt}
}

// validateNewToken checks a creation request and returns the trimmed name and the lifetime to use.
func (s *Service) validateNewToken(in NewToken) (name string, ttl time.Duration, err error) {
	var problems []FieldProblem
	name = strings.TrimSpace(in.Name)
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		problems = append(problems, FieldProblem{Field: "name", Code: CodeRequired})
	case n > maxTokenName || !cleanText(name):
		problems = append(problems, FieldProblem{Field: "name", Code: CodeInvalid})
	}
	switch in.Scope {
	case TokenScopeRead, TokenScopeAdmin:
	case "":
		problems = append(problems, FieldProblem{Field: "scope", Code: CodeRequired})
	default:
		problems = append(problems, FieldProblem{Field: "scope", Code: CodeInvalid})
	}
	ttl = s.tokenMaxTTLPersonal
	if in.ExpiresIn != nil {
		switch {
		case *in.ExpiresIn <= 0:
			problems = append(problems, FieldProblem{Field: "expires_in", Code: CodeInvalid})
		case *in.ExpiresIn > s.tokenMaxTTLPersonal:
			problems = append(problems, FieldProblem{Field: "expires_in", Code: CodeTooLarge})
		default:
			ttl = *in.ExpiresIn
		}
	}
	if len(problems) > 0 {
		return "", 0, &ValidationError{Problems: problems}
	}
	return name, ttl, nil
}

// CreateOwnToken creates a personal token for the caller and returns it with its secret. It is allowed only
// from a browser session: a request authenticated by a token (WithToken) is refused, so a leaked token cannot
// extend itself. The scope admin needs the caller to be a platform admin or to manage an initiative right now.
//
// The unit of work takes the lock of the owner's user row first (the lock SetUserStatus takes), reads the
// permissions and counts the live tokens under it, and only then inserts: the foreign key of the insert takes
// just KEY SHARE on the user, which does not conflict with a concurrent disable, so without the lock a token
// could be inserted after the disable revoked the others, and two parallel creations could pass the count
// together. A creation has no natural key: a retry after an ambiguous failure can leave a second token, which
// the client lists and revokes.
func (s *Service) CreateOwnToken(ctx context.Context, callerID string, in NewToken) (CreatedToken, error) {
	if TokenFrom(ctx) != "" {
		return CreatedToken{}, ErrForbidden
	}
	var out CreatedToken
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = CreatedToken{}
		u, err := tx.Users().LockUser(ctx, callerID)
		if errors.Is(err, ErrNotFound) { // unknown, or not a human: robots never create tokens here
			return ErrUserInactive
		}
		if err != nil {
			return fmt.Errorf("lock token owner: %w", err)
		}
		if u.Status != UserStatusActive {
			return ErrUserInactive
		}
		sub, _, err := begin(ctx, tx, callerID, adminz.TokenCreateOwn, adminz.Resource{Type: TargetToken})
		if err != nil {
			return err
		}
		name, ttl, err := s.validateNewToken(in)
		if err != nil {
			return err
		}
		if in.Scope == TokenScopeAdmin && !sub.PlatformAdmin && len(sub.Managed) == 0 {
			return invalid("scope", CodeNotAllowed)
		}
		live, err := tx.Tokens().CountLive(ctx, callerID)
		if err != nil {
			return fmt.Errorf("count live tokens: %w", err)
		}
		if live >= MaxLiveTokens {
			return ErrTokenLimit
		}
		secret, err := apitoken.Generate()
		if err != nil {
			return fmt.Errorf("generate token: %w", err)
		}
		tok, err := tx.Tokens().InsertToken(ctx, TokenRow{
			OwnerID: callerID, CreatedBy: callerID, Name: name, Scope: in.Scope, Hash: apitoken.Hash(secret), TTL: ttl,
		})
		if err != nil {
			return fmt.Errorf("insert token: %w", err)
		}
		if err := record(ctx, tx, event{
			actor: callerID, action: AuditTokenCreate, targetType: TargetToken, targetID: tok.ID, after: docOfToken(tok),
		}); err != nil {
			return err
		}
		out = CreatedToken{APIToken: tok, Secret: secret}
		return nil
	})
	return out, err
}

// ListOwnTokens lists the caller's tokens, newest first, including the recently dead ones.
func (s *Service) ListOwnTokens(ctx context.Context, callerID string, req PageRequest) (Page[APIToken], error) {
	size := pageSize(req.Limit)
	var out Page[APIToken]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[APIToken]{}
		if _, _, err := begin(ctx, tx, callerID, adminz.TokenListOwn, adminz.Resource{Type: TargetToken}); err != nil {
			return err
		}
		after, err := decodeCursor(cursorKindTokens, req.Cursor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Tokens().TokensOf(ctx, callerID, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list tokens: %w", err)
		}
		out = pageOf(rows, size, cursorKindTokens, func(t APIToken) Cursor { return Cursor{ID: t.ID} })
		return nil
	})
	return out, err
}

// RevokeOwnToken revokes one of the caller's tokens; a token of someone else is as absent as an unknown id.
func (s *Service) RevokeOwnToken(ctx context.Context, callerID, tokenID string) error {
	return s.revokeToken(ctx, callerID, tokenID, false)
}

// RevokeToken revokes a token of any owner: its owner can, and so can a platform admin. Anyone else gets
// ErrNotFound, so the existence of the token is not confirmed.
func (s *Service) RevokeToken(ctx context.Context, callerID, tokenID string) error {
	return s.revokeToken(ctx, callerID, tokenID, true)
}

// RevokeCurrentToken revokes the token that authenticates the request (whoever holds the secret can kill it).
// A request without a token, a browser session, gets ErrNotFound.
func (s *Service) RevokeCurrentToken(ctx context.Context, callerID string) error {
	id := TokenFrom(ctx)
	if id == "" {
		return ErrNotFound
	}
	return s.revokeToken(ctx, callerID, id, false)
}

// revokeToken is the one revocation. Revoking a token that is already revoked changes nothing, writes no audit
// row and succeeds, so a retry is harmless. anyOwner lets a platform admin revoke the token of another owner.
func (s *Service) revokeToken(ctx context.Context, callerID, tokenID string, anyOwner bool) error {
	tokenID, ok := canonicalUUID(tokenID)
	if !ok {
		return ErrNotFound
	}
	return s.store.WithTx(ctx, func(tx Tx) error {
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		tok, err := tx.Tokens().GetToken(ctx, tokenID)
		if err != nil {
			return err
		}
		action, res := adminz.TokenRevokeOwn, adminz.Resource{Type: TargetToken, ID: tokenID}
		if tok.UserID != callerID {
			if !anyOwner {
				return ErrNotFound
			}
			action = adminz.TokenRevokeAny
		}
		if _, err := authorize(sub, action, res); err != nil {
			if tok.UserID != callerID {
				return ErrNotFound // not an admin: do not confirm that the token exists
			}
			return err
		}
		changed, err := tx.Tokens().RevokeToken(ctx, tokenID)
		if err != nil {
			return fmt.Errorf("revoke token: %w", err)
		}
		if !changed {
			return nil
		}
		return record(ctx, tx, event{
			actor: callerID, action: AuditTokenRevoke, targetType: TargetToken, targetID: tokenID, before: docOfToken(tok),
		})
	})
}

// RevokeBySecret revokes the live token a secret belongs to (platform admins only, from a session or an admin
// token). A malformed secret is a 422, which reveals nothing about existence; a well-formed one that matches
// nothing succeeds exactly like one that matched (no existence oracle), without an audit row. Only a hit is
// audited, with the id of the token (never the secret).
func (s *Service) RevokeBySecret(ctx context.Context, callerID, secret string) error {
	return s.store.WithTx(ctx, func(tx Tx) error {
		if _, _, err := begin(ctx, tx, callerID, adminz.TokenRevokeAny, adminz.Resource{Type: TargetToken}); err != nil {
			return err
		}
		if secret == "" {
			return invalid("token", CodeRequired)
		}
		if !apitoken.WellFormed(secret) {
			return invalid("token", CodeInvalid)
		}
		id, err := tx.Tokens().RevokeByHash(ctx, apitoken.Hash(secret))
		if err != nil {
			return fmt.Errorf("revoke token by secret: %w", err)
		}
		if id == "" {
			return nil
		}
		tok, err := tx.Tokens().GetToken(ctx, id)
		if err != nil {
			return fmt.Errorf("read revoked token: %w", err)
		}
		return record(ctx, tx, event{
			actor: callerID, action: AuditTokenRevoke, targetType: TargetToken, targetID: id, before: docOfToken(tok),
		})
	})
}
