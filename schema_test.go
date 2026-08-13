package goflights

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/maxmwang/goflights/internal/pb"
)

// TestBookingTokenSchema guards the reverse-engineered response schema against
// drift. testdata/tokens.txt holds booking tokens captured from live result
// pages, one per distinct feature combination: every cabin, USD and JPY,
// one-way and round trip, one through three segments, and passenger parties of
// adults and children.
//
// Re-encoding and comparing byte length is the real assertion. A field the
// schema models with the wrong cardinality silently drops occurrences without
// registering as unknown, so a shorter re-encode is the only signal — that is
// how repeated `passengers` and the `airlines` name list were caught.
func TestBookingTokenSchema(t *testing.T) {
	raw, err := os.ReadFile("testdata/tokens.txt")
	if err != nil {
		t.Fatal(err)
	}

	for i, tok := range strings.Fields(string(raw)) {
		// Tokens are standard base64 with padding, unlike the URL-safe
		// unpadded encoding the tfs request parameter uses.
		b, err := base64.StdEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("token %d: decode: %v", i, err)
		}

		var bt pb.BookingToken
		if err := proto.Unmarshal(b, &bt); err != nil {
			t.Fatalf("token %d: unmarshal: %v", i, err)
		}

		if n := len(bt.ProtoReflect().GetUnknown()); n > 0 {
			t.Errorf("token %d: %d bytes of unmodelled fields", i, n)
		}

		out, err := proto.Marshal(&bt)
		if err != nil {
			t.Fatalf("token %d: marshal: %v", i, err)
		}
		if len(out) != len(b) {
			t.Errorf("token %d: re-encoded to %d bytes, want %d — schema is lossy",
				i, len(out), len(b))
		}
	}
}

func TestBookingTokenFields(t *testing.T) {
	raw, err := os.ReadFile("testdata/tokens.txt")
	if err != nil {
		t.Fatal(err)
	}
	tokens := strings.Fields(string(raw))

	var sawJPY, sawBusiness, sawMultiAirline, sawChild bool
	for _, tok := range tokens {
		b, err := base64.StdEncoding.DecodeString(tok)
		if err != nil {
			t.Fatal(err)
		}
		var bt pb.BookingToken
		if err := proto.Unmarshal(b, &bt); err != nil {
			t.Fatal(err)
		}

		if bt.GetCurrency() == "" {
			t.Error("missing currency")
		}
		// JPY quotes whole yen, so fraction_digits is an explicit zero.
		if bt.GetCurrency() == "JPY" {
			sawJPY = true
			if got := bt.GetDecimalDigits(); got != 0 {
				t.Errorf("JPY fraction_digits = %d, want 0", got)
			}
		}

		for _, s := range bt.GetTrip().GetSlices() {
			for _, seg := range s.GetSegments() {
				if seg.GetFromAirport() == "" || seg.GetToAirport() == "" {
					t.Error("segment missing airport code")
				}
				// Timestamps carry a UTC offset, so a bare date never appears.
				if !strings.Contains(seg.GetDepartureTime(), "T") {
					t.Errorf("departure %q is not ISO-8601", seg.GetDepartureTime())
				}
				if seg.GetClass() == pb.Class_CLASS_BUSINESS {
					sawBusiness = true
				}
			}
		}

		if len(bt.GetTrip().GetAirlines().GetNames()) > 1 {
			sawMultiAirline = true
		}
		for _, p := range bt.GetTrip().GetPassengers() {
			if p.GetType() == 2 {
				sawChild = true
			}
		}
	}

	// The fixture is only meaningful if it still spans these cases.
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"JPY", sawJPY},
		{"business cabin", sawBusiness},
		{"multi-airline itinerary", sawMultiAirline},
		{"child passenger", sawChild},
	} {
		if !c.ok {
			t.Errorf("fixture no longer covers %s", c.name)
		}
	}
}
