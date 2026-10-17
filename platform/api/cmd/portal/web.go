package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/Wysted/atrium/platform/api/internal/webui"
)

// webHandler builds the handler of the embedded SPA. A binary without it (a development or test build that never
// ran `nuxt generate`) logs one warning and returns nil, so non-API paths answer 404; any other failure (an HTML
// file the policy cannot express, or an invalid auth origin) is fatal. authOrigin is PORTAL_ISSUER, which
// config validation already requires to be an https origin: it is the auth host of the form-action directive.
func webHandler(authOrigin string, log *slog.Logger) (http.Handler, error) {
	fsys, err := webui.Embedded()
	if err != nil {
		return nil, err
	}
	return newWebHandler(fsys, authOrigin, log)
}

func newWebHandler(fsys fs.FS, authOrigin string, log *slog.Logger) (http.Handler, error) {
	h, err := webui.New(fsys, webui.Options{AuthOrigin: authOrigin})
	switch {
	case errors.Is(err, webui.ErrNotBuilt):
		log.Warn("the web UI is not embedded in this binary: non-API paths answer 404")
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("web UI: %w", err)
	}
	return h, nil
}
