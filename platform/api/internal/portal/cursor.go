package portal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// maxCursorLength bounds the text accepted as a cursor (real ones are about 100 bytes).
const maxCursorLength = 256

// Cursor is a keyset position: the last row of the previous page. Listings ordered by id alone leave At zero;
// the audit listing orders by (At, ID).
type Cursor struct {
	At time.Time
	ID string
}

// cursorDoc is the document inside the opaque text. Kind ties a cursor to the listing that issued it, so a
// cursor of one listing is rejected by another instead of silently paging through the wrong keys.
type cursorDoc struct {
	Kind string     `json:"k"`
	At   *time.Time `json:"t,omitempty"`
	ID   string     `json:"i"`
}

// encodeCursor returns the opaque text of c for a listing kind.
func encodeCursor(kind string, c Cursor) string {
	doc := cursorDoc{Kind: kind, ID: c.ID}
	if !c.At.IsZero() {
		at := c.At.UTC()
		doc.At = &at
	}
	b, err := json.Marshal(doc)
	if err != nil { // a fixed shape of strings and a time: cannot fail
		panic(fmt.Sprintf("portal: encode cursor: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses text issued by encodeCursor for the same kind. The empty text is the first page (nil).
// Anything else that is not exactly a cursor of that kind fails with ErrInvalidCursor: the text is client
// input, so it is checked as strictly as a body (no unknown fields, canonical ids, time only where the
// listing uses one).
func decodeCursor(kind, text string, withTime bool) (*Cursor, error) {
	if text == "" {
		return nil, nil
	}
	if len(text) > maxCursorLength {
		return nil, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var doc cursorDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, ErrInvalidCursor
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // trailing data
		return nil, ErrInvalidCursor
	}
	if doc.Kind != kind || !IsUUID(doc.ID) || (doc.At != nil) != withTime {
		return nil, ErrInvalidCursor
	}
	c := &Cursor{ID: strings.ToLower(doc.ID)}
	if doc.At != nil {
		c.At = *doc.At
	}
	return c, nil
}

// pageSize applies the default and the cap to a requested page size.
func pageSize(requested int) int {
	switch {
	case requested <= 0:
		return DefaultPageSize
	case requested > MaxPageSize:
		return MaxPageSize
	default:
		return requested
	}
}
