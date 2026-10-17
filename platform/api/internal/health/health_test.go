package health_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wysted/atrium/platform/api/internal/health"
	"github.com/Wysted/atrium/platform/api/internal/keys"
)

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

func TestReadyDBMigrations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pending []string
		err     error
		want    int
	}{
		{"all applied", nil, nil, http.StatusOK},
		{"pending", []string{"0015_x.sql"}, nil, http.StatusServiceUnavailable},
		{"check fails", nil, errors.New("boom"), http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := health.ReadyDB(okPinger{}, func(context.Context) ([]string, error) { return tt.pending, tt.err },
				slog.New(slog.NewTextHandler(io.Discard, nil)))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, health.PathReady, nil))
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

type okSigning struct{}

func (okSigning) Signer() (keys.Signer, error) { return keys.Signer{}, nil }
func (okSigning) KEKMismatch() bool            { return false }

func TestReadyPendingMigrations(t *testing.T) {
	t.Parallel()
	h := health.Ready(okPinger{}, func(context.Context) ([]string, error) { return []string{"0015_x.sql"}, nil },
		okSigning{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, health.PathReady, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
