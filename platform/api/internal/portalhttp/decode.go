package portalhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"platform/api/internal/httpx"
)

// MaxBodyBytes bounds a request body. API payloads are a handful of short fields.
const MaxBodyBytes = 64 << 10

// Details of the problem documents DecodeJSON writes. They are fixed texts: the decoder's own messages (which
// can quote input or change between Go versions) never reach the client.
const (
	detailBodyTooLarge = "request body too large"
	detailBodyEmpty    = "request body is required"
	detailBodyInvalid  = "request body must be a single JSON object with the documented fields"
)

// DecodeJSON reads the request body as exactly one JSON object into a T. The body is capped at MaxBodyBytes,
// unknown fields are rejected, and anything after the object is rejected too. On failure it writes the
// problem document (413 for an oversized body, 400 otherwise) and returns false; the caller just returns.
// Validation of the values (required fields, formats) belongs to the use case.
func DecodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var zero T
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			httpx.WriteProblem(w, httpx.Problem{Status: http.StatusRequestEntityTooLarge, Detail: detailBodyTooLarge})
		} else {
			httpx.WriteProblem(w, httpx.Problem{Status: http.StatusBadRequest, Detail: detailBodyInvalid})
		}
		return zero, false
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusBadRequest, Detail: detailBodyEmpty})
		return zero, false
	}
	// A JSON null (or an array, a string...) would decode into a zero value without complaint.
	if body[0] != '{' {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusBadRequest, Detail: detailBodyInvalid})
		return zero, false
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusBadRequest, Detail: detailBodyInvalid})
		return zero, false
	}
	// Trailing data: a second value, or garbage after the object.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusBadRequest, Detail: detailBodyInvalid})
		return zero, false
	}
	return v, true
}

// WriteJSON writes v as an application/json response with the given status. The marshaling happens before
// anything is written, so a failure still produces a clean 500.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusInternalServerError})
		return
	}
	w.Header().Set(httpx.HeaderContentType, mediaTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
