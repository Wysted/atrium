package httpx

import (
	"strings"
	"testing"
)

func TestListenerConflicts(t *testing.T) {
	for name, tc := range map[string]struct {
		a, b    string
		wantErr string // substring; empty means no error
	}{
		"wildcard vs 0.0.0.0":  {":8083", "0.0.0.0:8083", "different ports"},
		"specific vs wildcard": {"127.0.0.1:8083", ":8083", "different ports"},
		"different specific":   {"127.0.0.1:8083", "127.0.0.2:8083", ""},
		"ipv6 wildcard":        {"[::]:8083", ":8083", "different ports"},
		"same specific":        {"127.0.0.1:8083", "127.0.0.1:8083", "different ports"},
		"different ports":      {":8083", ":8084", ""},
		"missing port":         {"localhost", ":8083", "host:port"},
		"empty port":           {"localhost:", ":8083", "host:port"},
	} {
		t.Run(name, func(t *testing.T) {
			errs := ListenerConflicts([]NamedAddr{{"A", tc.a}, {"B", tc.b}})
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
				return
			}
			if len(errs) == 0 || !strings.Contains(errs[0].Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, errs)
			}
		})
	}
}
