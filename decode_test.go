package goflights

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// testdata/page.html mirrors the real page's structure at a fraction of the
// size: two AF_initDataCallback blocks so key selection is exercised, three
// live booking tokens spread across both itinerary sections, and each token
// buried in a nested JSON-encoded array the way Google nests them.
func TestDecode(t *testing.T) {
	page, err := os.ReadFile("testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}

	got, err := decode(string(page))
	if err != nil {
		t.Fatal(err)
	}
	// Two from section 2, one from section 3.
	if len(got) != 3 {
		t.Fatalf("decoded %d itineraries, want 3", len(got))
	}

	for i, it := range got {
		if it.GetCurrency() == "" {
			t.Errorf("itinerary %d: no currency", i)
		}
		if it.GetFare().GetAmount() == 0 {
			t.Errorf("itinerary %d: no fare", i)
		}
		segs := it.GetTrip().GetSlices()[0].GetSegments()
		if len(segs) == 0 {
			t.Fatalf("itinerary %d: no segments", i)
		}
		for _, s := range segs {
			if len(s.GetFromAirport()) != 3 || len(s.GetToAirport()) != 3 {
				t.Errorf("itinerary %d: %q->%q are not IATA codes",
					i, s.GetFromAirport(), s.GetToAirport())
			}
			if !strings.Contains(s.GetDepartureTime(), "T") {
				t.Errorf("itinerary %d: %q is not ISO-8601", i, s.GetDepartureTime())
			}
			if s.GetAirline() == "" || s.GetFlightNumber() == "" {
				t.Errorf("itinerary %d: missing flight number", i)
			}
		}
	}
}

// testdata/page_empty.html is the real block Google returns for a date in the
// past: a payload whose leading element is a bare status code instead of an
// array, and which carries no sideChannel key at all.
func TestDecodeNoFlights(t *testing.T) {
	page, err := os.ReadFile("testdata/page_empty.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decode(string(page)); !errors.Is(err, ErrNoFlights) {
		t.Errorf("err = %v, want ErrNoFlights", err)
	}
}

// A malformed request produces a second, much terser error payload than a date
// in the past does. Both are recognised by the leading status code rather than
// by anything they have in common textually.
func TestDecodeNoFlightsMinimal(t *testing.T) {
	page := `AF_initDataCallback({key: 'ds:1',  data:[3],errorHasStatus: true,});`

	if _, err := decode(page); !errors.Is(err, ErrNoFlights) {
		t.Errorf("err = %v, want ErrNoFlights", err)
	}
}

// The data argument is delimited by matching brackets, so what trails it does
// not matter — a block ending in sideChannel, in errorHasStatus, or in nothing
// at all must yield the same payload.
func TestInitDataIgnoresTrailingKeys(t *testing.T) {
	const want = `[null,[1,2],"x"]`

	for _, trailer := range []string{
		`, sideChannel: {}});`,
		`,errorHasStatus: true,});`,
		`});`,
		`, hash: '9', somethingNew: [1,2,3]});`,
	} {
		got, err := initDataJSON(`AF_initDataCallback({key: 'ds:1', data:`+want+trailer, "ds:1")
		if err != nil {
			t.Errorf("trailer %q: %v", trailer, err)
			continue
		}
		if got != want {
			t.Errorf("trailer %q: got %s, want %s", trailer, got, want)
		}
	}
}

// Itinerary records embed brackets and escaped quotes inside strings — the
// booking token arrives as a serialised array — so depth counting has to
// ignore anything within a string literal.
func TestArrayLenSkipsStringLiterals(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want int
	}{
		{"brackets in string", `["][[[", 1]tail`, 11},
		{"escaped quote", `["a\"]b", 2]tail`, 12},
		{"nested arrays", `[[1,[2,[3]]],4]tail`, 15},
		{"escaped backslash", `["a\\", 5]tail`, 10},
	} {
		got, err := arrayLen(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %d (%q), want %d", tc.name, got, tc.in[:got], tc.want)
		}
	}

	for _, bad := range []string{"", "{}", `[1,2`, `["unterminated`} {
		if _, err := arrayLen(bad); err == nil {
			t.Errorf("arrayLen(%q) = nil error, want failure", bad)
		}
	}
}

func TestDecodeMissingBlock(t *testing.T) {
	if _, err := decode("<html><body>consent interstitial</body></html>"); err == nil {
		t.Error("want an error when the results block is absent")
	}
}

// A record too short to reach the token index, or carrying something else
// there, must be skipped rather than guessed at: decoding the token is what
// establishes it was one.
func TestDecodeIgnoresNonTokens(t *testing.T) {
	page := `AF_initDataCallback({key: 'ds:1', hash: '1', data:` +
		`[null,null,[[["not-a-token","YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXphYmNkZWZnaGlqaw=="]]],null]` +
		`, sideChannel: {}});`

	got, err := decode(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("decoded %d itineraries from junk, want 0", len(got))
	}
}
