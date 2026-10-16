package httpx

import "net/http"

// StatusRecorder remembers the status code of a response (the first one written). Unwrap keeps
// http.ResponseController working.
type StatusRecorder struct {
	http.ResponseWriter
	// Status is zero until the handler writes a header or a body.
	Status int
}

// WriteHeader implements http.ResponseWriter.
func (s *StatusRecorder) WriteHeader(code int) {
	if s.Status == 0 {
		s.Status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (s *StatusRecorder) Write(b []byte) (int, error) {
	if s.Status == 0 {
		s.Status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap returns the wrapped writer.
func (s *StatusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// StatusOrOK is the recorded status, http.StatusOK when the handler wrote nothing.
func (s *StatusRecorder) StatusOrOK() int {
	if s.Status == 0 {
		return http.StatusOK
	}
	return s.Status
}
