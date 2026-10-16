package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/hostname"
	"platform/api/internal/testdb"
)

// hostCases is shared by hostname.Valid and the deployments.host CHECK: both must agree on every entry.
// NUL is left out: PostgreSQL text cannot hold it, so the INSERT fails for a different reason.
func hostCases() []string {
	l63 := strings.Repeat("a", hostname.MaxLabelLength)
	host253 := l63 + "." + l63 + "." + l63 + "." + strings.Repeat("b", hostname.MaxLength-3*(hostname.MaxLabelLength+1))
	return []string{
		"portal.apps.example.test", "a", "a.b", "a-b.c0.example", "1example.test", "xn--bcher-kva.example",
		"xn--", "a.xn--p1ai", "0-0.test", "a1", "9a", "example.1a", "a.b-1",
		l63 + ".example", l63 + "a.example", host253, host253 + "b", host253 + "bb",
		strings.Repeat("a.", 126) + "a", // 253 bytes, many labels
		"", ".", "..", "a.", ".a", "a..b", "a..", "..a", "-a", "a-", "-a.example", "a-.example", "a.-b", "a.b-",
		"-", "a.-", "Portal.example", "PORTAL", "a.Example", "*.example.test", "*", "a_b.example", "_a.example",
		"example.test:8443", ":80", "exa mple.test", " a", "a ", "a\tb", "example.test/", "a/b", "a@b", "a%41",
		"bücher.example", "ü", "例え.jp", "a.example\n", "\na.example", "a\r.example", "a.example\r\n",
		"10.0.0.1", "1.2.3.4", "999", "0", "example.123", "a.1", "a.00", "1.2.3.a", "1.2.3.4a", "a.1.b", "1.a",
		"[::1]", "::1", "a,b", "a;b", "a!b", "a+b", "a~b", "a\\b", "a'b", `a"b`, "a(b)", "a#b", "a?b",
	}
}

func TestDeploymentsHostCheckMatchesHostnameValid(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	portal := testdb.Portal(t, owner)

	iid := scalar[string](t, owner, `INSERT INTO initiatives (key) VALUES ('hostcheck') RETURNING id::text`)
	mustExec(t, owner, `INSERT INTO apps (id, initiative_id) VALUES ('app_01HZZZZZZZZZZZZZZZZZZZZZZZ', $1)`, iid)

	const insert = `INSERT INTO deployments (host, app_id) VALUES ($1, 'app_01HZZZZZZZZZZZZZZZZZZZZZZZ')`
	for _, host := range hostCases() {
		want := hostname.Valid(host)
		for _, pool := range []struct {
			name string
			db   *pgxpool.Pool
		}{{"owner", owner}, {"portal", portal}} {
			err := exec(pool.db, insert, host)
			var pgErr *pgconn.PgError
			switch {
			case err == nil:
				if !want {
					t.Errorf("%s: CHECK accepted %q but hostname.Valid refuses it", pool.name, host)
				}
				mustExec(t, owner, `DELETE FROM deployments WHERE host = $1`, host) // the next role inserts it again
			case errors.As(err, &pgErr) && pgErr.Code == codeCheckViolation:
				if want {
					t.Errorf("%s: CHECK refused %q but hostname.Valid accepts it", pool.name, host)
				}
			default:
				t.Errorf("%s: insert %q: unexpected error %v", pool.name, host, err)
			}
		}
	}
}
