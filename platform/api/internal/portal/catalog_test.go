package portal

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestValidHost(t *testing.T) {
	t.Parallel()
	label63 := strings.Repeat("a", maxLabelLength)
	// 3 labels of 63 plus a short one: 63*3 + 3 dots + 59 = 251; build exactly 253 and 254 bytes.
	host253 := label63 + "." + label63 + "." + label63 + "." + strings.Repeat("b", 253-3*64)
	tests := []struct {
		host string
		want bool
	}{
		{"portal.apps.example.test", true},
		{"a", true},
		{"xn--bcher-kva.example", true},
		{"a-b.c0.example", true},
		{label63 + ".example", true},
		{host253, true},
		{host253 + "b", false},
		{label63 + "a.example", false},
		{"", false},
		{"Portal.example.test", false},
		{"portal.example.test.", false},
		{"*.example.test", false},
		{"a..example", false},
		{".example", false},
		{"-a.example", false},
		{"a-.example", false},
		{"a_b.example", false},
		{"example.test:8443", false},
		{"exa mple.test", false},
		{"example.test/", false},
		{"bücher.example", false},
		{"10.0.0.1", false},
		{"example.123", false},
		{"1example.test", true},
		{"a.example\x00", false},
		{"a.example\n", false},
	}
	for _, tt := range tests {
		if got := validHost(tt.host); got != tt.want {
			t.Errorf("validHost(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
	if len(host253) != maxHostLength {
		t.Fatalf("test host has %d bytes, want %d", len(host253), maxHostLength)
	}
}

var appIDSchemaCheck = regexp.MustCompile(`^app_[0-9A-HJKMNP-TV-Z]{26}$`)

func TestNewAppID(t *testing.T) {
	t.Parallel()
	zeros, ones := bytes.NewReader(make([]byte, ulidRandomBytes)), bytes.NewReader(bytes.Repeat([]byte{0xff}, ulidRandomBytes))
	tests := []struct {
		name    string
		at      time.Time
		entropy func() *bytes.Reader
		want    string
	}{
		{"epoch and zero entropy", time.UnixMilli(0), func() *bytes.Reader { return zeros }, "app_" + "0000000000" + "0000000000000000"},
		{"largest time and full entropy", time.UnixMilli(maxULIDTime), func() *bytes.Reader { return ones }, "app_" + "7ZZZZZZZZZ" + "ZZZZZZZZZZZZZZZZ"},
		{"one millisecond", time.UnixMilli(1), func() *bytes.Reader { return bytes.NewReader(make([]byte, ulidRandomBytes)) }, "app_" + "0000000001" + "0000000000000000"},
		{"32 milliseconds carry", time.UnixMilli(32), func() *bytes.Reader { return bytes.NewReader(make([]byte, ulidRandomBytes)) }, "app_" + "0000000010" + "0000000000000000"},
		{"entropy fills the low bits", time.UnixMilli(0), func() *bytes.Reader {
			return bytes.NewReader([]byte{0, 0, 0, 0, 1, 0, 0, 0, 0, 31})
		}, "app_" + "0000000000" + "00000001" + "0000000Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := newAppID(tt.at, tt.entropy())
			if err != nil || got != tt.want {
				t.Fatalf("newAppID = %q, %v; want %q", got, err, tt.want)
			}
			if !appIDSchemaCheck.MatchString(got) || !appIDPattern.MatchString(got) {
				t.Fatalf("%q does not match the apps.id CHECK", got)
			}
		})
	}
}

func TestNewAppIDErrors(t *testing.T) {
	t.Parallel()
	if _, err := newAppID(time.UnixMilli(-1), bytes.NewReader(make([]byte, ulidRandomBytes))); !errors.Is(err, errULIDTime) {
		t.Errorf("negative time: %v", err)
	}
	if _, err := newAppID(time.UnixMilli(maxULIDTime+1), bytes.NewReader(make([]byte, ulidRandomBytes))); !errors.Is(err, errULIDTime) {
		t.Errorf("time beyond 48 bits: %v", err)
	}
	if _, err := newAppID(time.Now(), bytes.NewReader(make([]byte, ulidRandomBytes-1))); err == nil {
		t.Error("short entropy accepted")
	}
	boom := errors.New("boom")
	if _, err := newAppID(time.Now(), iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Errorf("failing entropy: %v", err)
	}
}

// The ids the system generates are valid and sort by creation time (ULIDs of different milliseconds).
func TestGeneratedAppIDs(t *testing.T) {
	t.Parallel()
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	prev := ""
	for i := range 200 {
		id, err := generateAppID(at.Add(time.Duration(i) * time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if !appIDSchemaCheck.MatchString(id) || seen[id] || id <= prev {
			t.Fatalf("id %q (previous %q, seen %v) is invalid, repeated or out of order", id, prev, seen[id])
		}
		seen[id], prev = true, id
	}
}

func TestRemovesLastManager(t *testing.T) {
	t.Parallel()
	g := func(id, role string, effective bool) ManagerGrant {
		return ManagerGrant{ID: id, RoleID: role, Effective: effective}
	}
	tests := []struct {
		name     string
		managers []ManagerGrant
		want     bool
	}{
		{"the only effective grant is of the role", []ManagerGrant{g("1", "r1", true)}, true},
		{"another role keeps an effective manager", []ManagerGrant{g("1", "r1", true), g("2", "r2", true)}, false},
		{"another grant of the same role does not count", []ManagerGrant{g("1", "r1", true), g("2", "r1", true)}, true},
		{"the other role's grant is not effective", []ManagerGrant{g("1", "r1", true), g("2", "r2", false)}, true},
		{"the role holds nothing effective", []ManagerGrant{g("1", "r1", false), g("2", "r2", true)}, false},
		{"the role holds nothing effective and nobody does", []ManagerGrant{g("1", "r1", false)}, false},
		{"the role has no grants", []ManagerGrant{g("2", "r2", true)}, false},
		{"no manager grants at all", nil, false},
		{"one effective among several of the role", []ManagerGrant{g("1", "r1", false), g("2", "r1", true), g("3", "r2", false)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := removesLastManager(tt.managers, "r1"); got != tt.want {
				t.Fatalf("removesLastManager = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateCatalogPatches(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	long := strings.Repeat("x", maxInitiativeName+1)
	tests := []struct {
		name  string
		patch InitiativePatch
		app   AppPatch
		want  []FieldProblem // nil: valid
	}{
		{"name", InitiativePatch{Name: str("New")}, AppPatch{Name: str("New")}, nil},
		{"empty name is allowed", InitiativePatch{Name: str("")}, AppPatch{Name: str("")}, nil},
		{"nothing to change", InitiativePatch{}, AppPatch{}, []FieldProblem{{"name", CodeAtLeastOne}, {"status", CodeAtLeastOne}}},
		{"name too long", InitiativePatch{Name: &long}, AppPatch{Name: &long}, []FieldProblem{{"name", CodeInvalid}}},
		{"NUL in the name", InitiativePatch{Name: str("a\x00b")}, AppPatch{Name: str("a\x00b")}, []FieldProblem{{"name", CodeInvalid}}},
		{"invalid UTF-8 in the name", InitiativePatch{Name: str("a\xffb")}, AppPatch{Name: str("a\xffb")}, []FieldProblem{{"name", CodeInvalid}}},
		{"empty status", InitiativePatch{Status: str("")}, AppPatch{Status: str("")}, []FieldProblem{{"status", CodeRequired}}},
		{"unknown status", InitiativePatch{Status: str("disabled")}, AppPatch{Status: str("inactive")}, []FieldProblem{{"status", CodeInvalid}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for what, err := range map[string]error{"initiative": validateInitiativePatch(tt.patch), "app": validateAppPatch(tt.app)} {
				if tt.want == nil {
					if err != nil {
						t.Errorf("%s: err = %v", what, err)
					}
					continue
				}
				v, ok := errors.AsType[*ValidationError](err)
				if !ok || len(v.Problems) != len(tt.want) {
					t.Fatalf("%s: err = %v, want problems %v", what, err, tt.want)
				}
				for i := range tt.want {
					if v.Problems[i] != tt.want[i] {
						t.Fatalf("%s: problems = %v, want %v", what, v.Problems, tt.want)
					}
				}
			}
		})
	}
	// The valid status of each resource.
	if err := validateInitiativePatch(InitiativePatch{Status: str(InitiativeStatusInactive)}); err != nil {
		t.Errorf("inactive initiative: %v", err)
	}
	if err := validateAppPatch(AppPatch{Status: str(AppStatusDisabled)}); err != nil {
		t.Errorf("disabled app: %v", err)
	}
}

func TestValidKey(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"platform": true, "a": true, "my-key_1": true, "0": true,
		"": false, "-a": false, "a-": false, "A": false, "a b": false, "a.b": false, "a\x00": false, "a\n": false,
		strings.Repeat("a", maxRoleKey): true, strings.Repeat("a", maxRoleKey+1): false,
	}
	for in, want := range tests {
		if got := validKey(in, maxRoleKey); got != want {
			t.Errorf("validKey(%q, %d) = %v, want %v", in, maxRoleKey, got, want)
		}
	}
}

func TestAppIDsInCursors(t *testing.T) {
	t.Parallel()
	const id = "app_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	text := encodeCursor(cursorKindApps, Cursor{ID: id})
	if c, err := decodeCursor(cursorKindApps, text, false); err != nil || c.ID != id {
		t.Fatalf("decode = %+v, %v", c, err)
	}
	for name, bad := range map[string]string{
		"a uuid on the apps listing": encodeCursor(cursorKindApps, Cursor{ID: idA}),
		"lowercase app id":           b64(`{"k":"apps","i":"app_01arz3ndektsv4rrffq69g5fav"}`),
		"letter U":                   b64(`{"k":"apps","i":"app_01ARZ3NDEKTSV4RRFFQ69G5FAU"}`),
		"too short":                  b64(`{"k":"apps","i":"app_01ARZ3"}`),
	} {
		if _, err := decodeCursor(cursorKindApps, bad, false); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: err = %v, want ErrInvalidCursor", name, err)
		}
	}
	if _, err := decodeCursor(cursorKindGrants, encodeCursor(cursorKindGrants, Cursor{ID: id}), false); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("an app id on a UUID listing: %v", err)
	}
}
