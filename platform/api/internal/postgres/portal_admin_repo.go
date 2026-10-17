package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"platform/api/internal/adminz"
	"platform/api/internal/audit"
	"platform/api/internal/portal"
)

// SQLSTATE codes the administration repositories translate.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
)

// writeError maps the constraint violations a write can hit to the errors of package portal; anything else is
// wrapped with what was being done. The server's message (which can quote values) is dropped on purpose.
func writeError(what string, err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pgErr.Code {
		case sqlStateUniqueViolation:
			return fmt.Errorf("%s: %w", what, portal.ErrDuplicate)
		case sqlStateForeignKeyViolation:
			return fmt.Errorf("%s: %w", what, &portal.ReferenceError{Field: referenceField(pgErr.ConstraintName)})
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// referenceFields maps the foreign keys that writes of the administration can violate (the names PostgreSQL
// derived from their columns) to the request field behind them. Exact names, not substrings.
var referenceFields = map[string]string{
	"initiative_access_initiative_id_role_id_fkey": "role_id",
	"initiative_access_user_id_fkey":               "user_id",
	"initiative_access_group_id_fkey":              "group_id",
	"group_members_group_id_fkey":                  "group_id",
	"group_members_user_id_fkey":                   "user_id",
	"initiative_roles_initiative_id_fkey":          "initiative_id",
	"apps_initiative_id_fkey":                      "initiative_id",
	"deployments_app_id_fkey":                      "app_id",
}

// referenceField names the request field behind a violated foreign key ("reference" for an unknown one).
func referenceField(constraint string) string {
	if f, ok := referenceFields[constraint]; ok {
		return f
	}
	return "reference"
}

// scopeArgs turns a Scope into the two parameters of the filter ($n::bool OR col = ANY($m::uuid[])). Every
// scoped listing starts from this one function, and each repository method returns before querying when the
// scope is empty, so the zero Scope lists nothing.
func scopeArgs(s adminz.Scope) (all bool, initiatives []string) {
	initiatives = s.Initiatives
	if initiatives == nil {
		initiatives = []string{}
	}
	return s.All, initiatives
}

// afterID returns the id of the keyset position, or nil for the first page.
func afterID(c *portal.Cursor) *string {
	if c == nil {
		return nil
	}
	return &c.ID
}

// afterTime returns the time of the keyset position, or nil for the first page.
func afterTime(c *portal.Cursor) *time.Time {
	if c == nil {
		return nil
	}
	return &c.At
}

// txAudit appends audit rows inside a transaction.
type txAudit struct{ db dbtx }

var _ audit.Recorder = txAudit{}

// RecordAudit implements audit.Recorder.
func (a txAudit) RecordAudit(ctx context.Context, e audit.Event) error {
	return insertAudit(ctx, a.db, e)
}

// ---- Grants ----

// GrantsRepo implements portal.GrantRepo.
type GrantsRepo struct{ db dbtx }

var _ portal.GrantRepo = GrantsRepo{}

const grantSelectSQL = `
SELECT ia.id::text, ia.initiative_id::text, ir.id::text, ir.key, ir.manages_access,
       COALESCE(ia.user_id::text, ''), COALESCE(u.email, ''), COALESCE(u.name, ''),
       COALESCE(ia.group_id::text, ''), COALESCE(g.key, ''), COALESCE(g.name, ''),
       ia.expires_at, COALESCE(ia.granted_by::text, ''), ia.granted_at
FROM initiative_access ia
JOIN initiative_roles ir ON ir.initiative_id = ia.initiative_id AND ir.id = ia.role_id
LEFT JOIN users u  ON u.id = ia.user_id
LEFT JOIN groups g ON g.id = ia.group_id`

func scanGrant(row pgx.CollectableRow) (portal.Grant, error) {
	var (
		g                          portal.Grant
		userID, userEmail, userNm  string
		groupID, groupKey, groupNm string
	)
	if err := row.Scan(&g.ID, &g.InitiativeID, &g.Role.ID, &g.Role.Key, &g.Role.ManagesAccess,
		&userID, &userEmail, &userNm, &groupID, &groupKey, &groupNm, &g.ExpiresAt, &g.GrantedBy, &g.GrantedAt); err != nil {
		return portal.Grant{}, err
	}
	if userID != "" {
		g.User = &portal.UserRef{ID: userID, Email: userEmail, Name: userNm}
	}
	if groupID != "" {
		g.Group = &portal.GroupRef{ID: groupID, Key: groupKey, Name: groupNm}
	}
	return g, nil
}

// InitiativeExists implements portal.GrantRepo.
func (r GrantsRepo) InitiativeExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM initiatives WHERE id = $1::uuid)`, id).Scan(&ok); err != nil {
		return false, fmt.Errorf("query initiative: %w", err)
	}
	return ok, nil
}

// GetGrant implements portal.GrantRepo.
func (r GrantsRepo) GetGrant(ctx context.Context, id string) (portal.Grant, error) {
	rows, err := r.db.Query(ctx, grantSelectSQL+` WHERE ia.id = $1::uuid`, id)
	if err != nil {
		return portal.Grant{}, fmt.Errorf("query grant: %w", err)
	}
	g, err := pgx.CollectOneRow(rows, scanGrant)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.Grant{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.Grant{}, fmt.Errorf("read grant: %w", err)
	}
	return g, nil
}

// ListGrants implements portal.GrantRepo: oldest first by id, restricted to the scope.
func (r GrantsRepo) ListGrants(ctx context.Context, scope adminz.Scope, q portal.PageQuery) ([]portal.Grant, error) {
	if scope.Empty() {
		return nil, nil
	}
	all, ids := scopeArgs(scope)
	rows, err := r.db.Query(ctx, grantSelectSQL+`
WHERE ($1::bool OR ia.initiative_id = ANY($2::uuid[]))
  AND ($3::uuid IS NULL OR ia.id > $3::uuid)
ORDER BY ia.id
LIMIT $4`, all, ids, afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query grants: %w", err)
	}
	grants, err := pgx.CollectRows(rows, scanGrant)
	if err != nil {
		return nil, fmt.Errorf("read grants: %w", err)
	}
	return grants, nil
}

// InsertGrant implements portal.GrantRepo.
func (r GrantsRepo) InsertGrant(ctx context.Context, row portal.GrantRow) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `
INSERT INTO initiative_access (initiative_id, role_id, user_id, group_id, expires_at, granted_by)
VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5::timestamptz, NULLIF($6, '')::uuid)
RETURNING id::text`, row.InitiativeID, row.RoleID, row.UserID, row.GroupID, row.ExpiresAt, row.GrantedBy).Scan(&id)
	if err != nil {
		return "", writeError("insert grant", err)
	}
	return id, nil
}

// DeleteGrant implements portal.GrantRepo.
func (r GrantsRepo) DeleteGrant(ctx context.Context, id string) (bool, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM initiative_access WHERE id = $1::uuid`, id)
	if err != nil {
		return false, fmt.Errorf("delete grant: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// LockInitiative implements portal.GrantRepo. FOR NO KEY UPDATE conflicts with itself, which is what
// serializes revocations, but not with the key-share locks that foreign keys to the initiative take.
func (r GrantsRepo) LockInitiative(ctx context.Context, id string) error {
	var locked string
	err := r.db.QueryRow(ctx, `SELECT id::text FROM initiatives WHERE id = $1::uuid FOR NO KEY UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock initiative: %w", err)
	}
	return nil
}

// effectiveGrantExpr is the definition of an effective grant, shared by the last-manager rule (managerGrantsSQL)
// and the integration health counts: not expired (by the database clock read now, after any lock wait, hence
// clock_timestamp() and not the transaction's now()) and held by at least one active user, directly or through
// a group. It needs the alias ia (initiative_access).
const effectiveGrantExpr = `(ia.expires_at IS NULL OR ia.expires_at > clock_timestamp())
       AND (EXISTS (SELECT 1 FROM users u WHERE u.id = ia.user_id AND u.status = 'active')
            OR EXISTS (SELECT 1 FROM group_members gm JOIN users u ON u.id = gm.user_id
                       WHERE gm.group_id = ia.group_id AND u.status = 'active'))`

// managerGrantsSQL locks the manages_access grants of an initiative, ordered by id, and computes whether each
// is effective (effectiveGrantExpr). $1 = initiative id.
const managerGrantsSQL = `
SELECT ia.id::text, ia.role_id::text,
       ` + effectiveGrantExpr + `
FROM initiative_access ia
JOIN initiative_roles ir ON ir.initiative_id = ia.initiative_id AND ir.id = ia.role_id
WHERE ia.initiative_id = $1::uuid AND ir.manages_access
ORDER BY ia.id
FOR UPDATE OF ia`

// LockInitiativeShared implements portal.GrantRepo.
func (r GrantsRepo) LockInitiativeShared(ctx context.Context, id string) error {
	var locked string
	err := r.db.QueryRow(ctx, `SELECT id::text FROM initiatives WHERE id = $1::uuid FOR SHARE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("share-lock initiative: %w", err)
	}
	return nil
}

// LockManagerGrants implements portal.GrantRepo.
func (r GrantsRepo) LockManagerGrants(ctx context.Context, initiativeID string) ([]portal.ManagerGrant, error) {
	rows, err := r.db.Query(ctx, managerGrantsSQL, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("query manager grants: %w", err)
	}
	grants, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (portal.ManagerGrant, error) {
		var m portal.ManagerGrant
		err := row.Scan(&m.ID, &m.RoleID, &m.Effective)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("read manager grants: %w", err)
	}
	return grants, nil
}

// ---- Groups and members ----

// GroupsRepo implements portal.GroupRepo.
type GroupsRepo struct{ db dbtx }

var _ portal.GroupRepo = GroupsRepo{}

const groupColumns = `id::text, key, name, created_at`

func scanGroup(row pgx.CollectableRow) (portal.Group, error) {
	var g portal.Group
	err := row.Scan(&g.ID, &g.Key, &g.Name, &g.CreatedAt)
	return g, err
}

// likeEscaper escapes the LIKE metacharacters so a search is always literal text.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// containsPattern turns a search into an ILIKE substring pattern ("" stays "": no filter). The pattern cannot
// use an index (leading wildcard), which is fine for a table of a few hundred groups.
func containsPattern(search string) string {
	if search == "" {
		return ""
	}
	return "%" + likeEscaper.Replace(search) + "%"
}

// ListGroups implements portal.GroupRepo. Groups are global, so only Scope.All lists them.
func (r GroupsRepo) ListGroups(ctx context.Context, scope adminz.Scope, q portal.PageQuery) ([]portal.Group, error) {
	if !scope.All {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `SELECT `+groupColumns+` FROM groups
WHERE ($1::uuid IS NULL OR id > $1::uuid)
  AND ($3::text = '' OR key ILIKE $3::text ESCAPE '\' OR name ILIKE $3::text ESCAPE '\')
ORDER BY id LIMIT $2`, afterID(q.After), q.Limit, containsPattern(q.Search))
	if err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	groups, err := pgx.CollectRows(rows, scanGroup)
	if err != nil {
		return nil, fmt.Errorf("read groups: %w", err)
	}
	return groups, nil
}

func (r GroupsRepo) one(ctx context.Context, suffix, id string) (portal.Group, error) {
	rows, err := r.db.Query(ctx, `SELECT `+groupColumns+` FROM groups WHERE id = $1::uuid`+suffix, id)
	if err != nil {
		return portal.Group{}, fmt.Errorf("query group: %w", err)
	}
	g, err := pgx.CollectOneRow(rows, scanGroup)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.Group{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.Group{}, fmt.Errorf("read group: %w", err)
	}
	return g, nil
}

// GetGroup implements portal.GroupRepo.
func (r GroupsRepo) GetGroup(ctx context.Context, id string) (portal.Group, error) {
	return r.one(ctx, "", id)
}

// LockGroup implements portal.GroupRepo.
func (r GroupsRepo) LockGroup(ctx context.Context, id string) (portal.Group, error) {
	return r.one(ctx, " FOR UPDATE", id)
}

// InsertGroup implements portal.GroupRepo.
func (r GroupsRepo) InsertGroup(ctx context.Context, key, name string) (portal.Group, error) {
	rows, err := r.db.Query(ctx, `INSERT INTO groups (key, name) VALUES ($1, $2) RETURNING `+groupColumns, key, name)
	if err != nil {
		return portal.Group{}, writeError("insert group", err)
	}
	g, err := pgx.CollectOneRow(rows, scanGroup)
	if err != nil {
		return portal.Group{}, writeError("insert group", err)
	}
	return g, nil
}

// GroupUsage implements portal.GroupRepo.
func (r GroupsRepo) GroupUsage(ctx context.Context, id string) (members, grants int, err error) {
	err = r.db.QueryRow(ctx, `
SELECT (SELECT count(*) FROM group_members WHERE group_id = $1::uuid),
       (SELECT count(*) FROM initiative_access WHERE group_id = $1::uuid)`, id).Scan(&members, &grants)
	if err != nil {
		return 0, 0, fmt.Errorf("query group usage: %w", err)
	}
	return members, grants, nil
}

// DeleteGroup implements portal.GroupRepo.
func (r GroupsRepo) DeleteGroup(ctx context.Context, id string) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM groups WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	return nil
}

// ListMembers implements portal.GroupRepo. Who is in a group is global data: only Scope.All lists it.
func (r GroupsRepo) ListMembers(ctx context.Context, scope adminz.Scope, groupID string, q portal.PageQuery) ([]portal.GroupMember, error) {
	if !scope.All {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
SELECT u.id::text, u.email, u.name, u.status, gm.added_at
FROM group_members gm JOIN users u ON u.id = gm.user_id
WHERE gm.group_id = $1::uuid AND ($2::uuid IS NULL OR gm.user_id > $2::uuid)
ORDER BY gm.user_id
LIMIT $3`, groupID, afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query group members: %w", err)
	}
	members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (portal.GroupMember, error) {
		var m portal.GroupMember
		err := row.Scan(&m.UserID, &m.Email, &m.Name, &m.Status, &m.AddedAt)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("read group members: %w", err)
	}
	return members, nil
}

// InsertMember implements portal.GroupRepo.
func (r GroupsRepo) InsertMember(ctx context.Context, groupID, userID string) (time.Time, error) {
	var at time.Time
	err := r.db.QueryRow(ctx, `INSERT INTO group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid) RETURNING added_at`,
		groupID, userID).Scan(&at)
	if err != nil {
		return time.Time{}, writeError("insert group member", err)
	}
	return at, nil
}

// DeleteMember implements portal.GroupRepo.
func (r GroupsRepo) DeleteMember(ctx context.Context, groupID, userID string) (bool, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM group_members WHERE group_id = $1::uuid AND user_id = $2::uuid`, groupID, userID)
	if err != nil {
		return false, fmt.Errorf("delete group member: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ---- Users ----

// UsersRepo implements portal.UserRepo.
type UsersRepo struct{ db dbtx }

var _ portal.UserRepo = UsersRepo{}

const userColumns = `id::text, email, name, status, created_at`

func scanUser(row pgx.CollectableRow) (portal.User, error) {
	var u portal.User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Status, &u.CreatedAt)
	return u, err
}

// ListUsersByEmail implements portal.UserRepo. Users are global: only Scope.All lists them.
func (r UsersRepo) ListUsersByEmail(ctx context.Context, scope adminz.Scope, email string, limit int) ([]portal.User, error) {
	if !scope.All {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1) ORDER BY id LIMIT $2`, email, limit)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	users, err := pgx.CollectRows(rows, scanUser)
	if err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	return users, nil
}

// maxActiveMatches is how many active users FindActiveByEmail looks at: two tell "exactly one" from "several".
const maxActiveMatches = 2

// FindActiveByEmail implements portal.UserRepo.
func (r UsersRepo) FindActiveByEmail(ctx context.Context, email string) ([]portal.User, error) {
	rows, err := r.db.Query(ctx, `SELECT `+userColumns+` FROM users
WHERE lower(email) = lower($1) AND status = 'active' ORDER BY id LIMIT $2`, email, maxActiveMatches)
	if err != nil {
		return nil, fmt.Errorf("query active users: %w", err)
	}
	users, err := pgx.CollectRows(rows, scanUser)
	if err != nil {
		return nil, fmt.Errorf("read active users: %w", err)
	}
	return users, nil
}

func (r UsersRepo) one(ctx context.Context, suffix, id string) (portal.User, error) {
	rows, err := r.db.Query(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1::uuid`+suffix, id)
	if err != nil {
		return portal.User{}, fmt.Errorf("query user: %w", err)
	}
	u, err := pgx.CollectOneRow(rows, scanUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.User{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.User{}, fmt.Errorf("read user: %w", err)
	}
	return u, nil
}

// GetUser implements portal.UserRepo.
func (r UsersRepo) GetUser(ctx context.Context, id string) (portal.User, error) {
	return r.one(ctx, "", id)
}

// LockUser implements portal.UserRepo.
func (r UsersRepo) LockUser(ctx context.Context, id string) (portal.User, error) {
	return r.one(ctx, " FOR NO KEY UPDATE", id)
}

// LockActiveAdmins implements portal.UserRepo.
func (r UsersRepo) LockActiveAdmins(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT u.id::text FROM users u JOIN platform_admins pa ON pa.user_id = u.id
WHERE u.status = 'active' ORDER BY u.id FOR NO KEY UPDATE OF u`)
	if err != nil {
		return nil, fmt.Errorf("lock platform admins: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read platform admins: %w", err)
	}
	return ids, nil
}

// SetStatus implements portal.UserRepo; the portal role may update only status and updated_at.
func (r UsersRepo) SetStatus(ctx context.Context, id, status string) error {
	if _, err := r.db.Exec(ctx, `UPDATE users SET status = $2, updated_at = now() WHERE id = $1::uuid`, id, status); err != nil {
		return fmt.Errorf("update user status: %w", err)
	}
	return nil
}

// DeleteSessions implements portal.UserRepo: app sessions and login codes go with the central sessions through
// the foreign keys' cascade.
func (r UsersRepo) DeleteSessions(ctx context.Context, userID string) (int64, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1::uuid`, userID)
	if err != nil {
		return 0, fmt.Errorf("delete user sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---- Audit log ----

// AuditLogRepo implements portal.AuditReader.
type AuditLogRepo struct{ db dbtx }

var _ portal.AuditReader = AuditLogRepo{}

// ListAudit implements portal.AuditReader: newest first by (occurred_at, id), restricted to the scope. Events
// without an initiative (global targets) belong to Scope.All only.
func (r AuditLogRepo) ListAudit(ctx context.Context, scope adminz.Scope, q portal.PageQuery) ([]portal.AuditEvent, error) {
	if scope.Empty() {
		return nil, nil
	}
	all, ids := scopeArgs(scope)
	rows, err := r.db.Query(ctx, `
SELECT a.id::text, a.occurred_at, COALESCE(a.actor_id::text, ''), COALESCE(u.email, ''),
       COALESCE(a.initiative_id::text, ''), a.action, a.target_type, a.target_id, a.before, a.after
FROM audit_events a
LEFT JOIN users u ON u.id = a.actor_id
WHERE ($1::bool OR a.initiative_id = ANY($2::uuid[]))
  AND ($3::timestamptz IS NULL OR (a.occurred_at, a.id) < ($3::timestamptz, $4::uuid))
ORDER BY a.occurred_at DESC, a.id DESC
LIMIT $5`, all, ids, afterTime(q.After), afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query audit events: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (portal.AuditEvent, error) {
		var (
			e             portal.AuditEvent
			before, after []byte
		)
		err := row.Scan(&e.ID, &e.OccurredAt, &e.ActorID, &e.ActorEmail, &e.InitiativeID, &e.Action, &e.TargetType,
			&e.TargetID, &before, &after)
		e.Before, e.After = json.RawMessage(before), json.RawMessage(after)
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("read audit events: %w", err)
	}
	return events, nil
}
