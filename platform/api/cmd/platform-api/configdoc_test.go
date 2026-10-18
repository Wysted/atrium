package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Relative to this package directory (platform/api/cmd/platform-api).
const platformRoot = "../../.."

var (
	// configDocPaths are the English page and its Spanish copy; both must list the same variables as the code.
	configDocPaths = []string{
		"../../../../docs/reference/configuration.md",
		"../../../../docs/es/reference/configuration.md",
	}
	// envLiteralPattern matches a quoted variable name in Go source. A quoted literal that is not a variable
	// would be a false positive; none exists today (see the sorted set in the failure message).
	envLiteralPattern = regexp.MustCompile(`"((?:PLATFORM|PORTAL|HELLO)_[A-Z0-9_]+)"`)
	// docRowPattern matches a table row whose first cell is a backticked variable name.
	docRowPattern = regexp.MustCompile("(?m)^\\| `((?:PLATFORM|PORTAL|HELLO)_[A-Z0-9_]+)`")
	skipDirs      = []string{"node_modules", ".nuxt", ".output", "vendor"}
)

// codeEnvNames returns every quoted variable name in the non-test Go files under root.
func codeEnvNames(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && slices.Contains(skipDirs, d.Name()):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		src, err := os.ReadFile(path) //nolint:gosec // G304: path comes from walking the repo, not from input
		if err != nil {
			return err
		}
		for _, m := range envLiteralPattern.FindAllSubmatch(src, -1) {
			names = append(names, string(m[1]))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// TestConfigurationDocMatchesCode keeps docs/reference/configuration.md (and its Spanish copy) and the code in step
// in both directions.
func TestConfigurationDocMatchesCode(t *testing.T) {
	code := codeEnvNames(t, platformRoot)
	for _, path := range configDocPaths {
		t.Run(path, func(t *testing.T) { checkConfigDoc(t, path, code) })
	}
}

func checkConfigDoc(t *testing.T, configDocPath string, code []string) {
	t.Helper()
	raw, err := os.ReadFile(configDocPath) //nolint:gosec // G304: fixed doc paths
	if err != nil {
		t.Fatal(err)
	}
	var documented []string
	for _, m := range docRowPattern.FindAllStringSubmatch(string(raw), -1) {
		documented = append(documented, m[1])
	}
	slices.Sort(documented)
	documented = slices.Compact(documented)

	var problems []string
	for _, n := range code {
		if !slices.Contains(documented, n) {
			problems = append(problems, fmt.Sprintf("%-40s in code, missing from the doc", n))
		}
	}
	for _, n := range documented {
		if !slices.Contains(code, n) {
			problems = append(problems, fmt.Sprintf("%-40s in the doc, not in code (stale)", n))
		}
	}
	if len(problems) > 0 {
		t.Errorf("%s is out of date:\n%s", configDocPath, strings.Join(problems, "\n"))
	}
}
