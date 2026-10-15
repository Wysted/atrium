package postgres_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ulidAlphabet is Crockford base32, the alphabet app ids must use.
const ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var fixtureSeq atomic.Int64

// appID builds a valid app_<ULID> id from a sequence number.
func appID(n int64) string {
	b := []byte("00000000000000000000000000")
	for i := len(b) - 1; n > 0 && i >= 0; i-- {
		b[i] = ulidAlphabet[n%int64(len(ulidAlphabet))]
		n /= int64(len(ulidAlphabet))
	}
	return "app_" + string(b)
}

// world is the set of ids a test needs to build scenarios.
type world struct {
	pool *pgxpool.Pool
}

func (w world) user(t *testing.T, status string) string {
	t.Helper()
	n := fixtureSeq.Add(1)
	return scalar[string](t, w.pool,
		`INSERT INTO users (google_sub, email, name, status) VALUES ($1, $2, 'n', $3) RETURNING id::text`,
		fmt.Sprintf("sub-%d", n), fmt.Sprintf("u%d@example.com", n), status)
}

func (w world) group(t *testing.T, key string) string {
	t.Helper()
	return scalar[string](t, w.pool, `INSERT INTO groups (key) VALUES ($1) RETURNING id::text`, key)
}

func (w world) member(t *testing.T, groupID, userID string) {
	t.Helper()
	mustExec(t, w.pool, `INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, groupID, userID)
}

func (w world) initiative(t *testing.T, key, status string) string {
	t.Helper()
	return scalar[string](t, w.pool, `INSERT INTO initiatives (key, status) VALUES ($1, $2) RETURNING id::text`, key, status)
}

func (w world) role(t *testing.T, initiativeID, key string) string {
	t.Helper()
	return scalar[string](t, w.pool,
		`INSERT INTO initiative_roles (initiative_id, key) VALUES ($1, $2) RETURNING id::text`, initiativeID, key)
}

// app creates an app in the initiative with one deployment on host and returns the app id.
func (w world) app(t *testing.T, initiativeID, host string) string {
	t.Helper()
	id := appID(fixtureSeq.Add(1))
	mustExec(t, w.pool, `INSERT INTO apps (id, initiative_id) VALUES ($1, $2)`, id, initiativeID)
	mustExec(t, w.pool, `INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, host, id)
	return id
}

func (w world) grantUser(t *testing.T, initiativeID, roleID, userID, expires string) {
	t.Helper()
	mustExec(t, w.pool,
		`INSERT INTO initiative_access (initiative_id, role_id, user_id, expires_at) VALUES ($1, $2, $3, NULLIF($4, '')::timestamptz)`,
		initiativeID, roleID, userID, expires)
}

func (w world) grantGroup(t *testing.T, initiativeID, roleID, groupID string) {
	t.Helper()
	mustExec(t, w.pool,
		`INSERT INTO initiative_access (initiative_id, role_id, group_id) VALUES ($1, $2, $3)`,
		initiativeID, roleID, groupID)
}
