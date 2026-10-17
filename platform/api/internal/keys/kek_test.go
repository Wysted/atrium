package keys_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wysted/atrium/platform/api/internal/keys"
)

func TestKEKSealOpen(t *testing.T) {
	kek, _, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	otherKEK, _, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("private scalar bytes")
	sealed, err := kek.Seal("kid-a", plaintext)
	if err != nil {
		t.Fatal(err)
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01

	tests := []struct {
		name    string
		kek     *keys.KEK
		kid     string
		sealed  []byte
		wantErr bool
	}{
		{"round trip", kek, "kid-a", sealed, false},
		{"altered kid (AAD)", kek, "kid-b", sealed, true},
		{"wrong KEK", otherKEK, "kid-a", sealed, true},
		{"tampered ciphertext", kek, "kid-a", tampered, true},
		{"truncated", kek, "kid-a", sealed[:5], true},
		{"empty", kek, "kid-a", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.kek.Open(tt.kid, tt.sealed)
			if tt.wantErr {
				if !errors.Is(err, keys.ErrDecrypt) {
					t.Fatalf("Open() err = %v, want ErrDecrypt", err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, plaintext) {
				t.Fatalf("Open() = %q, %v", got, err)
			}
		})
	}
}

func TestKEKSealUsesFreshNonce(t *testing.T) {
	kek, _, _ := keys.GenerateKEK()
	a, _ := kek.Seal("k", []byte("same"))
	b, _ := kek.Seal("k", []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext are identical: nonce reused")
	}
}

func TestNewKEKSize(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		if _, err := keys.NewKEK(make([]byte, size)); err == nil {
			t.Errorf("NewKEK(%d bytes) accepted", size)
		}
	}
	if _, err := keys.NewKEK(bytes.Repeat([]byte{7}, keys.KEKSize)); err != nil {
		t.Errorf("NewKEK(32 bytes): %v", err)
	}
}

// L6: an all-zero KEK is a placeholder, never a key.
func TestNewKEKRejectsAllZeros(t *testing.T) {
	if _, err := keys.NewKEK(make([]byte, keys.KEKSize)); err == nil {
		t.Fatal("all-zero KEK accepted")
	}
}

// L6: sealed blobs start with the format version so the KEK format can evolve.
func TestSealedFormatIsVersioned(t *testing.T) {
	kek, _, _ := keys.GenerateKEK()
	sealed, err := kek.Seal("kid", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if sealed[0] != 1 {
		t.Fatalf("version byte = %d, want 1", sealed[0])
	}
	for _, version := range []byte{0, 2, 255} {
		forged := bytes.Clone(sealed)
		forged[0] = version
		if _, err := kek.Open("kid", forged); !errors.Is(err, keys.ErrDecrypt) {
			t.Errorf("version %d: err = %v, want ErrDecrypt", version, err)
		}
	}
}

func TestLoadKEKFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	short := filepath.Join(dir, "short")
	raw := bytes.Repeat([]byte{7}, keys.KEKSize)
	if err := os.WriteFile(good, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(short, raw[:10], 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := keys.LoadKEKFile(good); err != nil {
		t.Errorf("good file: %v", err)
	}
	zeros := filepath.Join(dir, "zeros")
	if err := os.WriteFile(zeros, make([]byte, keys.KEKSize), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.LoadKEKFile(zeros); err == nil {
		t.Error("all-zero file accepted")
	}
	if _, err := keys.LoadKEKFile(short); err == nil {
		t.Error("short file accepted")
	}
	if _, err := keys.LoadKEKFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestKEKIsRedacted(t *testing.T) {
	kek, raw, _ := keys.GenerateKEK()
	for _, s := range []string{fmt.Sprintf("%v", kek), fmt.Sprintf("%+v", kek), fmt.Sprintf("%#v", kek), fmt.Sprintf("%s", kek)} { //nolint:staticcheck // S1025: %s on purpose, the test asserts that every verb redacts the key
		if s != "KEK(redacted)" || bytes.Contains([]byte(s), raw) {
			t.Errorf("KEK formatted as %q", s)
		}
	}
}

// L6: a KEK file writable by group or accessible by others is rejected; group read is allowed (K8s fsGroup).
func TestLoadKEKFileRejectsOpenPermissions(t *testing.T) {
	raw := bytes.Repeat([]byte{7}, keys.KEKSize)
	tests := []struct {
		mode    os.FileMode
		wantErr bool
	}{
		{0o600, false}, {0o400, false}, {0o440, false}, {0o640, false}, {0o644, true}, {0o604, true}, {0o660, true}, {0o700, true},
	}
	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kek")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil { // chmod: WriteFile is subject to the umask
				t.Fatal(err)
			}
			_, err := keys.LoadKEKFile(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("mode %v: err = %v, wantErr %v", tt.mode, err, tt.wantErr)
			}
		})
	}
	if _, err := keys.LoadKEKFile(t.TempDir()); err == nil {
		t.Error("directory accepted as KEK file")
	}
}
