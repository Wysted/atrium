package main

import (
	"bytes"
	"log/slog"
	"testing"
	"testing/fstest"
)

const testAuthOrigin = "https://auth.example.test"

func TestNewWebHandler(t *testing.T) {
	t.Parallel()
	t.Run("not built warns once and leaves Web nil", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		h, err := newWebHandler(fstest.MapFS{".gitkeep": {}}, testAuthOrigin, slog.New(slog.NewTextHandler(&logs, nil)))
		if err != nil || h != nil {
			t.Fatalf("handler = %v, err = %v", h, err)
		}
		if bytes.Count(logs.Bytes(), []byte("level=WARN")) != 1 {
			t.Fatalf("logs = %q", logs.String())
		}
	})
	t.Run("any other failure is fatal", func(t *testing.T) {
		t.Parallel()
		fsys := fstest.MapFS{"200.html": {Data: []byte("<html><body>no script</body></html>")}}
		if h, err := newWebHandler(fsys, testAuthOrigin, slog.New(slog.DiscardHandler)); err == nil || h != nil {
			t.Fatalf("handler = %v, err = %v", h, err)
		}
	})
	t.Run("a built SPA is served", func(t *testing.T) {
		t.Parallel()
		fsys := fstest.MapFS{"200.html": {Data: []byte("<html><body><script>a()</script></body></html>")}}
		if h, err := newWebHandler(fsys, testAuthOrigin, slog.New(slog.DiscardHandler)); err != nil || h == nil {
			t.Fatalf("handler = %v, err = %v", h, err)
		}
	})
	t.Run("an invalid auth origin is fatal", func(t *testing.T) {
		t.Parallel()
		fsys := fstest.MapFS{"200.html": {Data: []byte("<html><body><script>a()</script></body></html>")}}
		if h, err := newWebHandler(fsys, "http://auth.example.test", slog.New(slog.DiscardHandler)); err == nil || h != nil {
			t.Fatalf("handler = %v, err = %v", h, err)
		}
	})
}
