package portal

import (
	"encoding/base64"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

const (
	idA = "01a14296-1449-7be0-957b-4b1ad8d7a001"
	idB = "01a14296-1449-7be0-957b-4b1ad8d7a002"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()
	at := time.Date(2030, 1, 2, 3, 4, 5, 123456000, time.FixedZone("x", 3600))
	tests := []struct {
		name     string
		kind     string
		in       Cursor
		withTime bool
	}{
		{"id only", cursorKindGrants, Cursor{ID: idA}, false},
		{"time and id", cursorKindAudit, Cursor{At: at, ID: idB}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			text := encodeCursor(tt.kind, tt.in)
			got, err := decodeCursor(tt.kind, text, tt.withTime)
			if err != nil || got == nil || got.ID != tt.in.ID || !got.At.Equal(tt.in.At) {
				t.Fatalf("decode = %+v, %v; want %+v", got, err, tt.in)
			}
		})
	}
	if c, err := decodeCursor(cursorKindGrants, "", false); c != nil || err != nil {
		t.Fatalf("empty cursor = %+v, %v; want the first page", c, err)
	}
}

func TestTamperedCursorsAreRejected(t *testing.T) {
	t.Parallel()
	valid := encodeCursor(cursorKindGrants, Cursor{ID: idA})
	audit := encodeCursor(cursorKindAudit, Cursor{At: time.Unix(1, 0), ID: idA})
	tests := []struct {
		name     string
		text     string
		kind     string
		withTime bool
	}{
		{"not base64", "!!!", cursorKindGrants, false},
		{"padding", valid + "=", cursorKindGrants, false},
		{"not json", b64("hello"), cursorKindGrants, false},
		{"empty object", b64("{}"), cursorKindGrants, false},
		{"another listing", valid, cursorKindGroups, false},
		{"audit cursor on an id listing", audit, cursorKindAudit, false},
		{"id cursor on the audit listing", encodeCursor(cursorKindAudit, Cursor{ID: idA}), cursorKindAudit, true},
		{"id is not a uuid", b64(`{"k":"grants","i":"1; DROP TABLE users"}`), cursorKindGrants, false},
		{"unknown field", b64(`{"k":"grants","i":"` + idA + `","x":1}`), cursorKindGrants, false},
		{"trailing data", b64(`{"k":"grants","i":"` + idA + `"} {}`), cursorKindGrants, false},
		{"bad time", b64(`{"k":"audit","t":"yesterday","i":"` + idA + `"}`), cursorKindAudit, true},
		{"too long", strings.Repeat("A", maxCursorLength+1), cursorKindGrants, false},
		{"flipped byte", valid[:len(valid)-2] + "AA", cursorKindGrants, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if c, err := decodeCursor(tt.kind, tt.text, tt.withTime); !errors.Is(err, ErrInvalidCursor) || c != nil {
				t.Fatalf("decode = %+v, %v; want ErrInvalidCursor", c, err)
			}
		})
	}
}

func TestPageSize(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]int{-1: DefaultPageSize, 0: DefaultPageSize, 1: 1, MaxPageSize: MaxPageSize, MaxPageSize + 1: MaxPageSize} {
		if got := pageSize(in); got != want {
			t.Errorf("pageSize(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestPageOf(t *testing.T) {
	t.Parallel()
	cur := func(s string) Cursor { return Cursor{ID: s} }
	if p := pageOf([]string{"a", "b"}, 2, "k", cur); len(p.Items) != 2 || p.NextCursor != "" {
		t.Fatalf("a full last page has no next cursor: %+v", p)
	}
	p := pageOf([]string{idA, idB, "extra"}, 2, cursorKindGrants, cur)
	if len(p.Items) != 2 || p.NextCursor == "" {
		t.Fatalf("page = %+v", p)
	}
	if c, err := decodeCursor(cursorKindGrants, p.NextCursor, false); err != nil || c.ID != idB {
		t.Fatalf("next cursor = %+v, %v; want the last row kept", c, err)
	}
}

func TestValidEmail(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"ada@example.com": true, "a.b+c@sub.example.com": true,
		"": false, "ada": false, "@example.com": false, "ada@": false, "a@b@c": false,
		"a da@example.com": false, "ada@exa\nmple.com": false, "ada@example.com\x00": false,
		strings.Repeat("a", maxEmailLength) + "@x.com": false,
	}
	for in, want := range tests {
		if got := validEmail(in); got != want {
			t.Errorf("validEmail(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidateNewGrant(t *testing.T) {
	t.Parallel()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s := &Service{now: func() time.Time { return now }}
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	tests := []struct {
		name string
		in   NewGrant
		want []FieldProblem // nil: valid
	}{
		{"user", NewGrant{Email: "a@example.com", RoleID: idA}, nil},
		{"group with expiry", NewGrant{GroupID: idB, RoleID: idA, ExpiresAt: &future}, nil},
		{"no subject", NewGrant{RoleID: idA}, []FieldProblem{{"email", CodeExclusive}, {"group_id", CodeExclusive}}},
		{"two subjects", NewGrant{Email: "a@example.com", GroupID: idB, RoleID: idA}, []FieldProblem{{"email", CodeExclusive}, {"group_id", CodeExclusive}}},
		{"group id is not a uuid", NewGrant{GroupID: "x", RoleID: idA}, []FieldProblem{{"group_id", CodeInvalid}}},
		{"no role", NewGrant{Email: "a@example.com"}, []FieldProblem{{"role_id", CodeRequired}}},
		{"role is not a uuid", NewGrant{Email: "a@example.com", RoleID: "'; --"}, []FieldProblem{{"role_id", CodeInvalid}}},
		{"expired", NewGrant{Email: "a@example.com", RoleID: idA, ExpiresAt: &past}, []FieldProblem{{"expires_at", CodeNotInTheFuture}}},
		{"expires right now", NewGrant{Email: "a@example.com", RoleID: idA, ExpiresAt: &now}, []FieldProblem{{"expires_at", CodeNotInTheFuture}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := s.validateNewGrant(tt.in)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			v, ok := errors.AsType[*ValidationError](err)
			if !ok || len(v.Problems) != len(tt.want) {
				t.Fatalf("err = %v, want problems %v", err, tt.want)
			}
			for i := range tt.want {
				if v.Problems[i] != tt.want[i] {
					t.Fatalf("problems = %v, want %v", v.Problems, tt.want)
				}
			}
		})
	}
}

func TestIsLastEffective(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		managers []ManagerGrant
		want     bool
	}{
		{"only effective grant", []ManagerGrant{{"g1", true}}, true},
		{"another effective one remains", []ManagerGrant{{"g1", true}, {"g2", true}}, false},
		{"others are not effective", []ManagerGrant{{"g1", true}, {"g2", false}, {"g3", false}}, true},
		{"the target is not effective", []ManagerGrant{{"g1", false}}, false},
		{"the target is not effective and nobody is", []ManagerGrant{{"g1", false}, {"g2", false}}, false},
		{"target missing from the list", []ManagerGrant{{"g2", true}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isLastEffective(tt.managers, "g1"); got != tt.want {
				t.Fatalf("isLastEffective = %v, want %v", got, tt.want)
			}
		})
	}
}

// Every listing method of the repository ports (named List*) takes the Scope the caller may see as its second
// parameter, after the context: a listing without one cannot be written, and the compiler enforces the type.
// This test makes sure a new List* method cannot be added to a port without it. FindActiveByEmail is
// intentionally outside the rule: it is not a listing but a lookup of at most two rows that resolves a grant's
// subject, and its name keeps it out of this check.
func TestListingMethodsTakeAScope(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "admin.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var listings int
	ast.Inspect(file, func(n ast.Node) bool {
		iface, ok := n.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, m := range iface.Methods.List {
			fn, ok := m.Type.(*ast.FuncType)
			if !ok || len(m.Names) == 0 || !strings.HasPrefix(m.Names[0].Name, "List") {
				continue
			}
			listings++
			if !secondParamIsScope(fn) {
				t.Errorf("%s: the second parameter of a listing must be adminz.Scope", m.Names[0].Name)
			}
		}
		return true
	})
	if listings < 5 { // grants, groups, members, users, audit
		t.Fatalf("found only %d listing methods: the check is not looking at the ports", listings)
	}
}

func secondParamIsScope(fn *ast.FuncType) bool {
	var types []ast.Expr
	for _, f := range fn.Params.List {
		for range max(len(f.Names), 1) {
			types = append(types, f.Type)
		}
	}
	if len(types) < 2 {
		return false
	}
	sel, ok := types[1].(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "adminz" && sel.Sel.Name == "Scope"
}

func TestIsOnlyAdmin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		admins []string
		want   bool
	}{
		{"the only active admin", []string{"a"}, true},
		{"another admin remains", []string{"a", "b"}, false},
		{"the target is not an active admin", []string{"b"}, false},
		{"no admin at all", nil, false},
	}
	for _, tt := range tests {
		if got := isOnlyAdmin(tt.admins, "a"); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}
