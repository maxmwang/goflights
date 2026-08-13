package goflights

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/maxmwang/goflights/internal/pb"
)

// ErrNoFlights is returned when Google reports the search produced nothing.
// The page still comes back 200.
var ErrNoFlights = errors.New("no flights found")

// initDataKey is the AF_initDataCallback block holding the results. The page
// ships several; the others are page chrome.
const initDataKey = "ds:1"

// Positions within the ds:1 payload. Google ships it as a positional array
// rather than an object, so every type below binds its fields by index in its
// own UnmarshalJSON. Naming them here keeps the layout in one place instead of
// scattered through the walk.
const (
	statusIndex       = 0 // a bare status code, present only on a rejected search
	bestFlightsIndex  = 2
	otherFlightsIndex = 3

	recordsIndex = 0 // a section wraps its record list one level deep
	tokenIndex   = 8 // the booking token's position within a record
)

// initData is the payload the page hands to AF_initDataCallback. Only the
// parts this package consumes are modelled; the other 28 top-level elements
// are airport metadata, airline lookup tables and page chrome.
type initData struct {
	// Status is set only when the search was rejected. On success the same
	// element is an array, which is how the two are told apart.
	Status *int

	BestFlights  section
	OtherFlights section
}

// section is one group of results. Both groups hold the same kind of record;
// they differ only in how Google ranked them.
type section struct {
	Records []record
}

// record is a single itinerary. Everything it carries beyond the token —
// airport display names, aircraft names, carbon emissions — is available in
// better form inside the token itself, so only the token is modelled.
type record struct {
	Token string
}

func (d *initData) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	if len(raw) > statusIndex {
		// json.Number rather than int: unmarshalling a null into an int is a
		// silent no-op, which would read as status zero.
		var status json.Number
		if err := json.Unmarshal(raw[statusIndex], &status); err == nil && status != "" {
			n, err := status.Int64()
			if err != nil {
				return fmt.Errorf("status %q: %w", status, err)
			}
			code := int(n)
			d.Status = &code
			return nil
		}
	}

	if len(raw) > bestFlightsIndex {
		if err := json.Unmarshal(raw[bestFlightsIndex], &d.BestFlights); err != nil {
			return fmt.Errorf("best flights: %w", err)
		}
	}
	if len(raw) > otherFlightsIndex {
		if err := json.Unmarshal(raw[otherFlightsIndex], &d.OtherFlights); err != nil {
			return fmt.Errorf("other flights: %w", err)
		}
	}
	return nil
}

func (s *section) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	// A section is null when it has no results at all, which is routine: a
	// selected outbound leg comes back with the best-flights group empty.
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) <= recordsIndex {
		return nil
	}
	return json.Unmarshal(raw[recordsIndex], &s.Records)
}

func (r *record) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) <= tokenIndex {
		return nil
	}

	// The token does not sit at that index directly: the element is a string
	// holding a serialised array, whose first entry is the token.
	var serialised string
	if err := json.Unmarshal(raw[tokenIndex], &serialised); err != nil {
		return nil
	}
	var wrapper []json.RawMessage
	if err := json.Unmarshal([]byte(serialised), &wrapper); err != nil || len(wrapper) == 0 {
		return nil
	}
	return json.Unmarshal(wrapper[0], &r.Token)
}

// decode pulls every itinerary's booking token out of a results page and
// unmarshals it. Results keep the order Google ranked them in.
func decode(page string) ([]*pb.BookingToken, error) {
	raw, err := initDataJSON(page, initDataKey)
	if err != nil {
		return nil, err
	}

	var data initData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	// Both a date in the past and a malformed selection come back as 3, which
	// is INVALID_ARGUMENT in Google's canonical status codes.
	if data.Status != nil {
		return nil, fmt.Errorf("%w (status %d)", ErrNoFlights, *data.Status)
	}

	var out []*pb.BookingToken
	for _, s := range []section{data.BestFlights, data.OtherFlights} {
		for _, rec := range s.Records {
			// Decoding is also the validity check: anything at that index
			// that is not a booking token fails to unmarshal and is skipped.
			if bt, ok := decodeToken(rec.Token); ok {
				out = append(out, bt)
			}
		}
	}
	return out, nil
}

// initDataJSON extracts the JSON array passed as the data argument of the
// AF_initDataCallback block with the given key.
//
// The block is a JavaScript call, not JSON — its keys are unquoted and the
// arguments that follow data vary — so the array is delimited by matching
// brackets rather than by whatever text comes after it.
func initDataJSON(page, key string) (string, error) {
	anchor := "key: '" + key + "'"
	i := strings.Index(page, anchor)
	if i < 0 {
		return "", fmt.Errorf("no %s block", key)
	}
	rest := page[i:]

	start := strings.Index(rest, "data:")
	if start < 0 {
		return "", fmt.Errorf("%s block has no data", key)
	}
	rest = strings.TrimLeft(rest[start+len("data:"):], " \t\r\n")

	n, err := arrayLen(rest)
	if err != nil {
		return "", fmt.Errorf("%s block: %w", key, err)
	}
	return rest[:n], nil
}

// arrayLen returns the length of the JSON array at the start of s. Bracket
// depth is tracked outside string literals only, since the itinerary records
// embed brackets inside strings — the booking token is wrapped in a nested
// array that has itself been serialised to a string.
func arrayLen(s string) (int, error) {
	if len(s) == 0 || s[0] != '[' {
		return 0, errors.New("data is not a JSON array")
	}

	var depth int
	var inString, escaped bool

	for i := 0; i < len(s); i++ {
		c := s[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '[':
			depth++
		case ']':
			if depth--; depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, errors.New("unterminated JSON array")
}

// decodeToken parses a candidate string as a booking token, reporting whether
// it actually was one. Tokens use standard padded base64 — unlike the tfs
// request parameter, which is URL-safe and unpadded.
func decodeToken(s string) (*pb.BookingToken, bool) {
	if len(s) < 40 {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}

	var bt pb.BookingToken
	if err := proto.Unmarshal(raw, &bt); err != nil {
		return nil, false
	}
	// Arbitrary bytes occasionally unmarshal without error, so require the
	// fields every real itinerary has.
	if bt.GetCurrency() == "" || len(bt.GetTrip().GetSlices()) == 0 {
		return nil, false
	}
	for _, s := range bt.GetTrip().GetSlices() {
		if len(s.GetSegments()) == 0 {
			return nil, false
		}
	}
	return &bt, true
}
