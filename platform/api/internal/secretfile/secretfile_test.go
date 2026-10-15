package secretfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secretContent = "s3cr3t-value-that-must-never-leak"

func writeSecret(t *testing.T, mode os.FileMode, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // chmod, not the WriteFile mode: the umask must not interfere
		t.Fatal(err)
	}
	return path
}

func TestReadPermissions(t *testing.T) {
	tests := []struct {
		mode os.FileMode
		ok   bool
	}{
		{0o400, true},
		{0o440, true},
		{0o600, true},
		{0o640, true},
		{0o644, false}, // world readable
		{0o660, false}, // group writable
		{0o604, false}, // other readable
		{0o666, false},
		{0o700, false}, // a secret is not executable: any bit outside 0640 is refused
	}
	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			got, err := Read(writeSecret(t, tt.mode, secretContent), 1024)
			if tt.ok {
				if err != nil || string(got) != secretContent {
					t.Fatalf("Read = %q, %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted a file that is too open")
			}
			if got != nil {
				t.Errorf("returned content with an error: %q", got)
			}
		})
	}
}

func TestReadRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, 1024); err == nil {
		t.Fatal("accepted a directory")
	}
	if _, err := Read(filepath.Join(dir, "missing"), 1024); err == nil {
		t.Fatal("accepted a missing file")
	}
}

func TestReadFollowsSymlinkToRegularFile(t *testing.T) {
	// Kubernetes mounts secrets through symlinks (..data); the checks run on the opened target.
	target := writeSecret(t, 0o440, secretContent)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(link, 1024); err != nil || string(got) != secretContent {
		t.Fatalf("Read through symlink = %q, %v", got, err)
	}
	open := writeSecret(t, 0o644, secretContent)
	link2 := filepath.Join(t.TempDir(), "link2")
	if err := os.Symlink(open, link2); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link2, 1024); err == nil {
		t.Fatal("a symlink must not hide a file that is too open")
	}
}

func TestReadSizeLimit(t *testing.T) {
	const limit = 16
	exact := strings.Repeat("a", limit)
	if got, err := Read(writeSecret(t, 0o400, exact), limit); err != nil || string(got) != exact {
		t.Fatalf("file of exactly the limit: %q, %v", got, err)
	}
	_, err := Read(writeSecret(t, 0o400, exact+"b"), limit)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("file over the limit: %v, want ErrTooLarge", err)
	}
	if got, err := Read(writeSecret(t, 0o400, ""), limit); err != nil || len(got) != 0 {
		t.Fatalf("empty file: %q, %v", got, err)
	}
}

// Whatever the failure, the error text names the problem and never the content of the file.
func TestReadErrorsNeverContainTheSecret(t *testing.T) {
	paths := map[string]string{
		"too open":  writeSecret(t, 0o644, secretContent),
		"too large": writeSecret(t, 0o400, secretContent),
	}
	for name, path := range paths {
		limit := 1024
		if name == "too large" {
			limit = len(secretContent) - 1
		}
		_, err := Read(path, limit)
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if bytes.Contains([]byte(err.Error()), []byte(secretContent)) || strings.Contains(err.Error(), secretContent[:8]) {
			t.Errorf("%s: error exposes the content: %v", name, err)
		}
	}
}
