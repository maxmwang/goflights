package goflights

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/maxmwang/goflights/internal/pb"
)

// loadConnecting returns a real itinerary with more than one segment, in the
// form a caller receives it, so the per-segment date has something to differ
// from.
func loadConnecting(t *testing.T) FlightOption {
	t.Helper()

	raw, err := os.ReadFile("testdata/tokens.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range strings.Fields(string(raw)) {
		b, err := base64.StdEncoding.DecodeString(tok)
		if err != nil {
			continue
		}
		var bt pb.BookingToken
		if err := proto.Unmarshal(b, &bt); err != nil {
			continue
		}
		opt, err := fromProto(&bt)
		if err != nil {
			continue
		}
		if len(opt.Segments) > 1 {
			return opt
		}
	}
	t.Skip("no connecting itinerary in testdata")
	return FlightOption{}
}

// optionOf wraps crafted segments, the way a caller assembling a selection by
// hand would.
func optionOf(segs ...FlightSegment) FlightOption {
	return FlightOption{Segments: segs}
}

func oneLeg() *FlightInfo {
	return NewFlightInfo().DepartureDateStr("2026-09-01").From("SFO").To("JFK")
}

func TestBuildRequest(t *testing.T) {
	req := NewRequest().
		Adults(2).Children(1).
		Class(ClassBusiness).
		MaxPrice(900).
		CheckedBag(2).
		ExcludeBasicEconomy().
		Flights(oneLeg().
			MaxStops(0).
			Airlines("AA", "UA").
			EarliestDepartureHour(8).
			LatestArrivalHour(22).
			MaxDuration(7 * time.Hour).
			MinLayover(45 * time.Minute).
			LessEmissions())

	got, err := req.build()
	if err != nil {
		t.Fatal(err)
	}

	if n := len(got.GetPassengers()); n != 3 {
		t.Errorf("passengers = %d, want 3", n)
	}
	if int32(got.GetClass()) != int32(ClassBusiness) {
		t.Errorf("class = %v", got.GetClass())
	}
	if got.GetMaxPrice() != 900 {
		t.Errorf("max price = %d, want 900", got.GetMaxPrice())
	}
	if got.GetBaggage().GetCheckedBag() != 2 {
		t.Errorf("checked bag = %d, want 2", got.GetBaggage().GetCheckedBag())
	}
	// Only the bag that was asked for should be constrained.
	if got.GetBaggage().CarryOnBag != nil {
		t.Error("carry-on bag set without being asked for")
	}
	// The builder never sets a trip type; the execution functions do.
	if got.GetTripType() != pb.TripType_TRIP_TYPE_UNSPECIFIED {
		t.Errorf("trip type = %v, want unspecified", got.GetTripType())
	}

	legs := got.GetFlights()
	if len(legs) != 1 {
		t.Fatalf("flights = %d, want 1", len(legs))
	}
	leg := legs[0]
	if leg.GetDate() != "2026-09-01" {
		t.Errorf("date = %q", leg.GetDate())
	}
	if leg.GetFrom()[0].GetCode() != "SFO" || leg.GetTo()[0].GetCode() != "JFK" {
		t.Errorf("route = %v -> %v", leg.GetFrom(), leg.GetTo())
	}
	// Zero is a real value here, so it has to survive as a set field.
	if leg.MaxStops == nil || *leg.MaxStops != 0 {
		t.Errorf("max stops = %v, want 0 set", leg.MaxStops)
	}
	if leg.GetMaxDurationMinutes() != 420 {
		t.Errorf("max duration = %d, want 420", leg.GetMaxDurationMinutes())
	}
	if leg.GetMinLayoverMinutes() != 45 {
		t.Errorf("min layover = %d, want 45", leg.GetMinLayoverMinutes())
	}
	if len(leg.GetEmissions()) != 1 {
		t.Errorf("emissions = %v", leg.GetEmissions())
	}
	if leg.GetLatestDepartureHour() != 0 || leg.LatestDepartureHour != nil {
		t.Errorf("unset hour leaked: %v", leg.LatestDepartureHour)
	}
}

func TestBuildErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *Request
		want string
	}{
		{"no flights", NewRequest().Adults(1), "no flights"},
		{"no date", NewRequest().Flights(NewFlightInfo().From("SFO").To("JFK")), "required field date unset"},
		{"required field from unset", NewRequest().Flights(NewFlightInfo().DepartureDateStr("2026-09-01").To("JFK")), "required field from unset"},
		{"required field to unset", NewRequest().Flights(NewFlightInfo().DepartureDateStr("2026-09-01").From("SFO")), "required field to unset"},
		{"bad date", NewRequest().Flights(oneLeg().DepartureDateStr("09/01/2026")), "departure date"},
		{"bad airport", NewRequest().Flights(oneLeg().From("SANFRAN")), "three-letter"},
		{"hour out of range", NewRequest().Flights(oneLeg().EarliestDepartureHour(24)), "outside 0-23"},
		{"inverted hours", NewRequest().Flights(oneLeg().EarliestDepartureHour(20).LatestDepartureHour(6)), "is after latest"},
		{"inverted layovers", NewRequest().Flights(oneLeg().MinLayover(3 * time.Hour).MaxLayover(time.Hour)), "longer than max"},
		{"negative duration", NewRequest().Flights(oneLeg().MaxDuration(-time.Hour)), "not positive"},
		{"negative party", NewRequest().Adults(-1).Flights(oneLeg()), "negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.req.build()
			if err == nil {
				t.Fatalf("built successfully, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The first error is the one that explains the mistake; later ones are
// consequences of it.
func TestFirstErrorWins(t *testing.T) {
	_, err := NewRequest().
		Flights(oneLeg().EarliestDepartureHour(99).MaxStops(-1)).
		build()
	if err == nil {
		t.Fatal("built successfully, want error")
	}
	if !strings.Contains(err.Error(), "outside 0-23") {
		t.Errorf("err = %q, want the first failure", err)
	}
}

// The intended flow: search, then hand an option straight back to SelectOption.
// The whole itinerary must survive to the wire, since a partial selection is
// silently ignored by Google and comes back as the original leg.
func TestSelectFromResponse(t *testing.T) {
	opt := loadConnecting(t)

	got, err := NewRequest().
		Adults(1).
		Flights(oneLeg().SelectOption(opt)).
		build()
	if err != nil {
		t.Fatalf("selection from a response rejected: %v", err)
	}

	sel := got.GetFlights()[0].GetSelectedSegments()
	if len(sel) != len(opt.Segments) {
		t.Fatalf("selected %d segments, want all %d", len(sel), len(opt.Segments))
	}

	for i, want := range opt.Segments {
		// Each entry carries its own segment's date, not the leg's: a late
		// connection departs the day after the first segment does.
		if date := want.DepartureTime.Format(time.DateOnly); sel[i].GetDate() != date {
			t.Errorf("segment %d: date = %q, want %q", i, sel[i].GetDate(), date)
		}
		if sel[i].GetFromAirport() != want.FromAirport || sel[i].GetToAirport() != want.ToAirport {
			t.Errorf("segment %d: %s->%s, want %s->%s", i,
				sel[i].GetFromAirport(), sel[i].GetToAirport(), want.FromAirport, want.ToAirport)
		}
		if sel[i].GetAirline() != want.Airline || sel[i].GetFlightNumber() != want.FlightNumber {
			t.Errorf("segment %d: %s%s, want %s%s", i,
				sel[i].GetAirline(), sel[i].GetFlightNumber(), want.Airline, want.FlightNumber)
		}
	}
}

// Selections accumulate, so a multi city leg can pin more than one already
// chosen itinerary. Passing several at once and calling repeatedly must come
// to the same thing.
func TestSelectOptionAccumulates(t *testing.T) {
	opt := loadConnecting(t)
	want := 2 * len(opt.Segments)

	chained := oneLeg().SelectOption(opt).SelectOption(opt)
	got, err := NewRequest().Adults(1).Flights(chained).build()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.GetFlights()[0].GetSelectedSegments()); n != want {
		t.Errorf("selecting twice gave %d segments, want %d", n, want)
	}

	variadic := oneLeg().SelectOption(opt, opt)
	got, err = NewRequest().Adults(1).Flights(variadic).build()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.GetFlights()[0].GetSelectedSegments()); n != want {
		t.Errorf("selecting both at once gave %d segments, want %d", n, want)
	}
}

// A zero FlightOption carries nothing to pin the leg with.
func TestSelectOptionRejectsEmpty(t *testing.T) {
	_, err := NewRequest().Adults(1).
		Flights(oneLeg().SelectOption(FlightOption{})).
		build()
	if err == nil {
		t.Fatal("built successfully, want an option with no segments rejected")
	}
	if !strings.Contains(err.Error(), "no segments") {
		t.Errorf("err = %q, want it to say the option had no segments", err)
	}
}

// A selection missing any field is silently ignored by Google, returning the
// wrong leg rather than an error, so it is caught before the request goes out.
// Callers pass segments straight from a response, so these only arise from a
// hand-built FlightSegment.
func TestSelectValidation(t *testing.T) {
	complete := FlightSegment{
		FromAirport:   "SFO",
		ToAirport:     "JFK",
		DepartureTime: time.Date(2026, 9, 1, 7, 43, 0, 0, time.UTC),
		ArrivalTime:   time.Date(2026, 9, 1, 16, 26, 0, 0, time.UTC),
		Airline:       "AS",
		FlightNumber:  "20",
	}
	if _, err := NewRequest().Adults(1).Flights(oneLeg().SelectOption(optionOf(complete))).build(); err != nil {
		t.Fatalf("complete selection rejected: %v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(*FlightSegment)
		want string
	}{
		{"no airline", func(s *FlightSegment) { s.Airline = "" }, "no airline"},
		{"no flight number", func(s *FlightSegment) { s.FlightNumber = "" }, "no flight number"},
		{"no from airport", func(s *FlightSegment) { s.FromAirport = "" }, "no from airport"},
		{"no to airport", func(s *FlightSegment) { s.ToAirport = "" }, "no to airport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := complete
			tc.mut(&s)
			_, err := NewRequest().Adults(1).Flights(oneLeg().SelectOption(optionOf(s))).build()
			if err == nil {
				t.Fatalf("built successfully, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A zero DepartureTime formats to "0001-01-01", a well-formed date, so the
// empty-date check in selectedSegment.build cannot catch it — the guard has to
// sit on the time.Time before it is formatted. Left through, Google ignores the
// whole selection and silently returns the original leg.
func TestSelectRejectsZeroDepartureTime(t *testing.T) {
	s := FlightSegment{
		FromAirport:  "SFO",
		ToAirport:    "JFK",
		ArrivalTime:  time.Date(2026, 9, 1, 16, 26, 0, 0, time.UTC),
		Airline:      "AS",
		FlightNumber: "20",
		// departureTime deliberately left zero.
	}

	_, err := NewRequest().Adults(1).Flights(oneLeg().SelectOption(optionOf(s))).build()
	if err == nil {
		t.Fatal("built successfully, want a zero departure time to be rejected")
	}
	if !strings.Contains(err.Error(), "departure time is unset") {
		t.Errorf("err = %q, want it to name the unset departure time", err)
	}
}

// Google server-renders only one itinerary for some parties and loads the rest
// over an RPC this package does not implement. The trigger is a property of
// the request because the truncated response is structurally identical to the
// reply to a leg selection. Thresholds measured against live searches.
func TestResultsDeferred(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *Request
		want bool
	}{
		{"single adult", NewRequest().Adults(1), false},
		{"four adults", NewRequest().Adults(4), false},
		{"two adults two children", NewRequest().Adults(2).Children(2), false},
		{"five adults", NewRequest().Adults(5), true},
		{"four adults one child", NewRequest().Adults(4).Children(1), true},
		// An infant triggers it regardless of how small the party is.
		{"adult and lap infant", NewRequest().Adults(1).InfantsOnLap(1), true},
		{"adult and seated infant", NewRequest().Adults(1).InfantsInSeat(1), true},
		{"no passengers", NewRequest(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.resultsDeferred(); got != tc.want {
				t.Errorf("resultsDeferred() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Setting a trip type turns the leg count into a checked constraint. Left
// unspecified the field stays off the request and the server infers the trip,
// which is why the unset case must still build.
func TestTripType(t *testing.T) {
	twoLegs := func() *Request {
		return NewRequest().Adults(1).Flights(
			oneLeg(),
			NewFlightInfo().DepartureDateStr("2026-09-08").From("JFK").To("SFO"),
		)
	}

	t.Run("unset omits the field", func(t *testing.T) {
		got, err := NewRequest().Adults(1).Flights(oneLeg()).build()
		if err != nil {
			t.Fatal(err)
		}
		if got.GetTripType() != pb.TripType_TRIP_TYPE_UNSPECIFIED {
			t.Errorf("trip type = %v, want unspecified", got.GetTripType())
		}
	})

	t.Run("explicitly unspecified is valid", func(t *testing.T) {
		got, err := twoLegs().TripType(TripTypeUnspecified).build()
		if err != nil {
			t.Fatalf("explicit unspecified rejected: %v", err)
		}
		// Unspecified must not constrain the leg count.
		if n := len(got.GetFlights()); n != 2 {
			t.Errorf("flights = %d, want 2", n)
		}
	})

	t.Run("one way reaches the wire", func(t *testing.T) {
		got, err := NewRequest().Adults(1).Flights(oneLeg()).TripType(TripTypeOneWay).build()
		if err != nil {
			t.Fatal(err)
		}
		if got.GetTripType() != pb.TripType_TRIP_TYPE_ONE_WAY {
			t.Errorf("trip type = %v, want one way", got.GetTripType())
		}
	})

	t.Run("round trip reaches the wire", func(t *testing.T) {
		got, err := twoLegs().TripType(TripTypeRoundTrip).build()
		if err != nil {
			t.Fatal(err)
		}
		if got.GetTripType() != pb.TripType_TRIP_TYPE_ROUND_TRIP {
			t.Errorf("trip type = %v, want round trip", got.GetTripType())
		}
	})

	// The leg count is only checked once a trip type says what to expect.
	t.Run("leg count mismatches", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			req  *Request
			want string
		}{
			{"one way with a return leg", twoLegs().TripType(TripTypeOneWay), "one FlightInfo"},
			{"round trip without a return leg",
				NewRequest().Adults(1).Flights(oneLeg()).TripType(TripTypeRoundTrip), "two FlightInfo"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := tc.req.build(); err == nil {
					t.Fatalf("built successfully, want error containing %q", tc.want)
				} else if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("err = %q, want it to contain %q", err, tc.want)
				}
			})
		}
	})

	t.Run("unknown value rejected", func(t *testing.T) {
		_, err := NewRequest().Adults(1).Flights(oneLeg()).TripType(TripType(99)).build()
		if err == nil {
			t.Fatal("built successfully, want an unknown trip type rejected")
		}
		if !strings.Contains(err.Error(), "not a known value") {
			t.Errorf("err = %q", err)
		}
	})
}

// Every field of FlightSegment is unexported, so the zero value is the only one
// a caller outside this package can produce. It must not be selectable — Google
// would silently ignore the selection and return the original leg.
func TestSelectOptionRejectsZeroSegment(t *testing.T) {
	_, err := NewRequest().Adults(1).
		Flights(oneLeg().SelectOption(optionOf(FlightSegment{}))).
		build()
	if err == nil {
		t.Fatal("built successfully, want the zero segment rejected")
	}
	if !strings.Contains(err.Error(), "no from airport") {
		t.Errorf("err = %q, want it to name the first missing field", err)
	}

	// A segment taken from a real response is accepted.
	if _, err := NewRequest().Adults(1).
		Flights(oneLeg().SelectOption(loadConnecting(t))).
		build(); err != nil {
		t.Errorf("segment from a response rejected: %v", err)
	}
}

// Every field the selection carries is checked, since Google ignores a
// selection it cannot match rather than reporting it.
func TestSelectOptionFieldValidation(t *testing.T) {
	complete := FlightSegment{
		FromAirport:   "SFO",
		ToAirport:     "JFK",
		DepartureTime: time.Date(2026, 9, 1, 7, 43, 0, 0, time.UTC),
		ArrivalTime:   time.Date(2026, 9, 1, 16, 26, 0, 0, time.UTC),
		Airline:       "AS",
		FlightNumber:  "20",
	}

	for _, tc := range []struct {
		name string
		mut  func(*FlightSegment)
		want string
	}{
		{"no from airport", func(s *FlightSegment) { s.FromAirport = "" }, "no from airport"},
		{"no to airport", func(s *FlightSegment) { s.ToAirport = "" }, "no to airport"},
		{"no airline", func(s *FlightSegment) { s.Airline = "" }, "no airline"},
		{"no flight number", func(s *FlightSegment) { s.FlightNumber = "" }, "no flight number"},
		{"malformed airport", func(s *FlightSegment) { s.FromAirport = "SANFRAN" }, "three-letter"},
		{"same airport twice", func(s *FlightSegment) { s.ToAirport = s.FromAirport }, "both"},
		{"zero departure", func(s *FlightSegment) { s.DepartureTime = time.Time{} }, "departure time is unset"},
		{"zero arrival", func(s *FlightSegment) { s.ArrivalTime = time.Time{} }, "arrival time is unset"},
		{"arrival before departure", func(s *FlightSegment) {
			s.ArrivalTime = s.DepartureTime.Add(-time.Hour)
		}, "not after departure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := complete
			tc.mut(&s)
			_, err := NewRequest().Adults(1).Flights(oneLeg().SelectOption(optionOf(s))).build()
			if err == nil {
				t.Fatalf("built successfully, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// Unset must mean absent on the wire, not a zero value written out, so that
// the server falls back to inferring the trip from the leg count.
func TestTripTypeAbsentWhenUnset(t *testing.T) {
	field := func(msg *pb.Request) bool {
		fd := msg.ProtoReflect().Descriptor().Fields().ByName("trip_type")
		return msg.ProtoReflect().Has(fd)
	}

	for _, tc := range []struct {
		name    string
		req     *Request
		present bool
	}{
		{"never called", NewRequest().Adults(1).Flights(oneLeg()), false},
		{"explicitly unspecified",
			NewRequest().Adults(1).Flights(oneLeg()).TripType(TripTypeUnspecified), false},
		{"explicitly one way",
			NewRequest().Adults(1).Flights(oneLeg()).TripType(TripTypeOneWay), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.req.build()
			if err != nil {
				t.Fatal(err)
			}
			if field(got) != tc.present {
				t.Errorf("trip_type present = %v, want %v", field(got), tc.present)
			}
		})
	}
}

// The locale reaches the wire through URL, which Execute also renders its
// request from, so setting it in one place covers both entry points.
func TestURLLocale(t *testing.T) {
	// Unset means absent, not a default, so Google applies its own.
	t.Run("unset params are omitted", func(t *testing.T) {
		u, err := NewRequest().Adults(1).Flights(oneLeg()).URL()
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		for _, param := range []string{"curr", "hl", "gl"} {
			if _, ok := q[param]; ok {
				t.Errorf("%s = %q, want it absent from the query", param, q.Get(param))
			}
		}
		if q.Get("tfs") == "" {
			t.Error("tfs missing")
		}
	})

	// Setting one must not drag the others in.
	t.Run("only the params that were set appear", func(t *testing.T) {
		u, err := NewRequest().Adults(1).Flights(oneLeg()).Currency("EUR").URL()
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if got := q.Get("curr"); got != "EUR" {
			t.Errorf("curr = %q, want EUR", got)
		}
		for _, param := range []string{"hl", "gl"} {
			if _, ok := q[param]; ok {
				t.Errorf("%s appeared without being set", param)
			}
		}
	})

	t.Run("overrides are normalised", func(t *testing.T) {
		u, err := NewRequest().Adults(1).Flights(oneLeg()).
			Currency("eur").Language("DE").Region("DE").
			URL()
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if got := q.Get("curr"); got != "EUR" {
			t.Errorf("curr = %q, want EUR upper-cased", got)
		}
		if got := q.Get("hl"); got != "de" {
			t.Errorf("hl = %q, want de lower-cased", got)
		}
		if got := q.Get("gl"); got != "de" {
			t.Errorf("gl = %q, want de lower-cased", got)
		}
	})

	// base64url's alphabet is unreserved, so the token must survive query
	// encoding byte for byte.
	t.Run("token is not escaped", func(t *testing.T) {
		req := NewRequest().Adults(1).Flights(oneLeg())
		u, err := req.URL()
		if err != nil {
			t.Fatal(err)
		}
		msg, err := req.build()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		want := base64.RawURLEncoding.EncodeToString(raw)
		if got := u.Query().Get("tfs"); got != want {
			t.Errorf("tfs = %q, want %q", got, want)
		}
		if strings.Contains(u.RawQuery, "%") {
			t.Errorf("query was percent-escaped: %s", u.RawQuery)
		}
	})

	t.Run("invalid codes rejected", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			req  *Request
			want string
		}{
			{"currency too long", NewRequest().Currency("DOLLARS"), "ISO 4217"},
			{"currency not alphabetic", NewRequest().Currency("US1"), "ISO 4217"},
			{"language too long", NewRequest().Language("eng"), "ISO 639-1"},
			{"region too short", NewRequest().Region("u"), "ISO 3166-1"},
			{"region with separator", NewRequest().Region("u&"), "ISO 3166-1"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := tc.req.Adults(1).Flights(oneLeg()).URL()
				if err == nil {
					t.Fatalf("built successfully, want error containing %q", tc.want)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("err = %q, want it to contain %q", err, tc.want)
				}
			})
		}
	})
}

// recordingTransport answers every request from a fixture, so the client can be
// exercised without reaching Google.
type recordingTransport struct {
	body     string
	requests []*http.Request
}

func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, r)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// ExecuteWith must route through the caller's client while behaving exactly as
// Execute otherwise, headers included.
func TestExecuteWith(t *testing.T) {
	page, err := os.ReadFile("testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	tr := &recordingTransport{body: string(page)}

	got, err := NewRequest().Adults(1).Currency("EUR").
		Flights(oneLeg()).
		ExecuteWith(context.Background(), &http.Client{Transport: tr}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// testdata/page.html carries three itineraries.
	if len(got) != 3 {
		t.Errorf("decoded %d itineraries, want 3", len(got))
	}
	if len(tr.requests) != 1 {
		t.Fatalf("client saw %d requests, want 1", len(tr.requests))
	}

	sent := tr.requests[0]
	// The package still supplies the headers Google needs; a caller's client
	// does not have to know about them.
	if ua := sent.Header.Get("User-Agent"); !strings.Contains(ua, "Mozilla") {
		t.Errorf("User-Agent = %q, want the browser one this package sets", ua)
	}
	// And the request the caller built is the one that went out.
	if curr := sent.URL.Query().Get("curr"); curr != "EUR" {
		t.Errorf("curr = %q, want EUR", curr)
	}
}

// A nil client is the documented way to mean "the default one".
func TestExecuteWithNilClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // fail before any network use, without asserting on the transport

	_, err := NewRequest().Adults(1).Flights(oneLeg()).ExecuteWith(ctx, nil, nil)
	if err == nil {
		t.Fatal("want the cancelled context to surface")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled — a nil client should have fallen back, not panicked", err)
	}
}

// Caller headers replace the default of the same name and are added alongside
// the rest, so an override yields one value rather than two.
func TestExecuteWithHeaders(t *testing.T) {
	page, err := os.ReadFile("testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		header http.Header
		want   map[string][]string
	}{
		{
			name:   "nil sends the defaults",
			header: nil,
			want: map[string][]string{
				"User-Agent":      {userAgent},
				"Accept-Language": {"en-US,en;q=0.9"},
			},
		},
		{
			name:   "override replaces rather than appends",
			header: http.Header{"User-Agent": {"my-crawler/1.0"}},
			want: map[string][]string{
				"User-Agent":      {"my-crawler/1.0"},
				"Accept-Language": {"en-US,en;q=0.9"},
			},
		},
		{
			name:   "extra headers are sent alongside",
			header: http.Header{"X-Trace-Id": {"abc123"}},
			want: map[string][]string{
				"User-Agent": {userAgent},
				"X-Trace-Id": {"abc123"},
			},
		},
		{
			// http.Header keys are canonicalised, so a lowercase key must
			// still override rather than land beside the default.
			name:   "non-canonical key still overrides",
			header: http.Header{"user-agent": {"lowercase/1.0"}},
			want:   map[string][]string{"User-Agent": {"lowercase/1.0"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &recordingTransport{body: string(page)}
			_, err := NewRequest().Adults(1).Flights(oneLeg()).
				ExecuteWith(context.Background(), &http.Client{Transport: tr}, tc.header)
			if err != nil {
				t.Fatal(err)
			}
			sent := tr.requests[0].Header
			for name, want := range tc.want {
				got := sent.Values(name)
				if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// The header map the caller passes must not be reachable from the request that
// goes out, or a later edit would change a search already made.
func TestExecuteWithHeadersNotAliased(t *testing.T) {
	page, err := os.ReadFile("testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	tr := &recordingTransport{body: string(page)}

	caller := http.Header{"User-Agent": {"first/1.0"}}
	if _, err := NewRequest().Adults(1).Flights(oneLeg()).
		ExecuteWith(context.Background(), &http.Client{Transport: tr}, caller); err != nil {
		t.Fatal(err)
	}

	caller["User-Agent"][0] = "mutated/2.0"
	if got := tr.requests[0].Header.Get("User-Agent"); got != "first/1.0" {
		t.Errorf("User-Agent = %q, want the value sent at the time", got)
	}
}
