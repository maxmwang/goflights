package goflights

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/maxmwang/goflights/internal/pb"
)

const (
	endpoint = "https://www.google.com/travel/flights"
)

// Class is the cabin to search. Google may still return a mix: a business
// search yields segments marked business or first.
type Class int32

const (
	ClassEconomy        Class = Class(pb.Class_CLASS_ECONOMY)
	ClassPremiumEconomy Class = Class(pb.Class_CLASS_PREMIUM_ECONOMY)
	ClassBusiness       Class = Class(pb.Class_CLASS_BUSINESS)
	ClassFirst          Class = Class(pb.Class_CLASS_FIRST)
)

// TripType is the shape of the trip. Leaving it unset is normal and usually
// correct: the server infers the trip from the number of legs, and a search
// sent without one returns the same results as the matching explicit value.
//
// Setting it turns the leg count into a checked constraint rather than an
// inference, which is what makes it worth setting. A one-way search carrying a
// stray return leg silently ignores it, and a round trip missing its return
// leg is priced against a return the caller never asked for.
type TripType int32

const (
	// TripTypeUnspecified leaves the trip type off the request entirely.
	TripTypeUnspecified TripType = TripType(pb.TripType_TRIP_TYPE_UNSPECIFIED)
	TripTypeRoundTrip   TripType = TripType(pb.TripType_TRIP_TYPE_ROUND_TRIP)
	TripTypeOneWay      TripType = TripType(pb.TripType_TRIP_TYPE_ONE_WAY)

	// TripTypeMultiCity is accepted but multi city searching does not work
	// yet. A search whose legs do not form a one way or a round trip comes
	// back empty, whatever the trip type, because Google leaves those results
	// out of the page entirely. See the package documentation.
	TripTypeMultiCity TripType = TripType(pb.TripType_TRIP_TYPE_MULTI_CITY)
)

// Passenger is one traveller in the party. Repeat a value to add more than one
// of that kind.
type Passenger int32

const (
	PassengerAdult        Passenger = Passenger(pb.Passenger_PASSENGER_ADULT)
	PassengerChild        Passenger = Passenger(pb.Passenger_PASSENGER_CHILD)
	PassengerInfantInSeat Passenger = Passenger(pb.Passenger_PASSENGER_INFANT_IN_SEAT)
	PassengerInfantOnLap  Passenger = Passenger(pb.Passenger_PASSENGER_INFANT_ON_LAP)
)

// selectedSegment pins one already-chosen segment of a leg, which makes the
// search return options for the next leg instead. Creating from FlightSegment
// through Select().
//
// Every field is required. Omitting Date is rejected outright; omitting any of
// the others makes the Google Flights server silently ignore the whole
// selection and return the original leg again, so they are checked before the
// request goes out.
type selectedSegment struct {
	fromAirport string
	toAirport   string

	// date is this segment's own departure date, YYYY-MM-DD. A late connection
	// lands the following day, so it is not always the leg's date.
	date string

	airline      string
	flightNumber string
}

func (s selectedSegment) build() (*pb.SelectedSegment, error) {
	// A selection missing any of these is either rejected or, worse, silently
	// ignored, so it is caught here rather than showing up as the wrong leg.
	for _, f := range []struct{ name, value string }{
		{"from airport", s.fromAirport},
		{"to airport", s.toAirport},
		{"date", s.date},
		{"airline", s.airline},
		{"flight number", s.flightNumber},
	} {
		if f.value == "" {
			return nil, fmt.Errorf("no %s", f.name)
		}
	}
	if _, err := time.Parse(time.DateOnly, s.date); err != nil {
		return nil, fmt.Errorf("date %q: %w", s.date, err)
	}
	return &pb.SelectedSegment{
		FromAirport:  s.fromAirport,
		ToAirport:    s.toAirport,
		Date:         s.date,
		Airline:      s.airline,
		FlightNumber: s.flightNumber,
	}, nil
}

// FlightInfo represents a request for one leg of a trip: where, when, and how.
// A one-way trip has a single leg, a round trip has two.
type FlightInfo struct {
	// Formatted as YYYY-MM-DD.
	date string

	// Segments already chosen for this leg, which makes the search return
	// options for the next leg instead. Empty on a first search.
	selectedSegments []selectedSegment

	// Maximum layovers, inclusive. nil is any number of layovers, represented by
	// null in protobuf.
	maxStops *int32
	airlines []string

	earliestDepartureHour *int32
	latestDepartureHour   *int32
	earliestArrivalHour   *int32
	latestArrivalHour     *int32

	maxDurationMinutes *int32

	from []string
	to   []string

	connectingAirports []string
	minLayoverMinutes  *int32
	maxLayoverMinutes  *int32

	lessEmissions bool

	// Stores first encountered error when building.
	err error
}

func NewFlightInfo() *FlightInfo {
	return &FlightInfo{}
}

// fail records the first error seen. Later ones are dropped: the first is the
// one that explains the mistake.
func (fi *FlightInfo) fail(err error) *FlightInfo {
	if fi.err == nil {
		fi.err = err
	}
	return fi
}

// DepartureDateStr sets the leg's date from a YYYY-MM-DD string.
func (fi *FlightInfo) DepartureDateStr(d string) *FlightInfo {
	if _, err := time.Parse(time.DateOnly, d); err != nil {
		return fi.fail(fmt.Errorf("departure date %q: %w", d, err))
	}
	fi.date = d
	return fi
}

// DepartureDate sets the leg's date, discarding the time of day.
func (fi *FlightInfo) DepartureDate(d time.Time) *FlightInfo {
	fi.date = d.Format(time.DateOnly)
	return fi
}

// SelectOption pins an itinerary already chosen for this leg, which makes the
// search return options for the next leg instead. Used when querying for round
// trip and multi city itineraries.
//
// The option must come from a previous search. Taking the whole option rather
// than its segments is what makes the selection complete: Google silently
// ignores one it cannot match in full, returning the original leg rather than
// an error.
//
// Selections accumulate, so a multi city leg can pin more than one already
// chosen itinerary.
func (fi *FlightInfo) SelectOption(options ...FlightOption) *FlightInfo {
	for i, option := range options {
		if len(option.Segments) == 0 {
			return fi.fail(fmt.Errorf("selected option %d: no segments", i))
		}
		for j, s := range option.Segments {
			if err := s.validate(); err != nil {
				return fi.fail(fmt.Errorf("selected option %d segment %d: %w", i, j, err))
			}
			fi.selectedSegments = append(fi.selectedSegments, s.toSelectedSegment())
		}
	}
	return fi
}

// MaxStops caps the connections on the leg. Zero means nonstop only.
func (fi *FlightInfo) MaxStops(n int32) *FlightInfo {
	if n < 0 {
		return fi.fail(fmt.Errorf("max stops: %d is negative", n))
	}
	fi.maxStops = &n
	return fi
}

// Airlines restricts the leg to the given IATA airline codes.
func (fi *FlightInfo) Airlines(codes ...string) *FlightInfo {
	fi.airlines = codes
	return fi
}

// EarliestDepartureHour keeps only flights leaving at or after the given local
// hour, 0 to 23.
func (fi *FlightInfo) EarliestDepartureHour(h int32) *FlightInfo {
	return fi.setHour(&fi.earliestDepartureHour, "earliest departure hour", h)
}

// LatestDepartureHour keeps only flights leaving at or before the given local
// hour, 0 to 23.
func (fi *FlightInfo) LatestDepartureHour(h int32) *FlightInfo {
	return fi.setHour(&fi.latestDepartureHour, "latest departure hour", h)
}

// EarliestArrivalHour keeps only flights landing at or after the given local
// hour, 0 to 23.
func (fi *FlightInfo) EarliestArrivalHour(h int32) *FlightInfo {
	return fi.setHour(&fi.earliestArrivalHour, "earliest arrival hour", h)
}

// LatestArrivalHour keeps only flights landing at or before the given local
// hour, 0 to 23.
func (fi *FlightInfo) LatestArrivalHour(h int32) *FlightInfo {
	return fi.setHour(&fi.latestArrivalHour, "latest arrival hour", h)
}

func (fi *FlightInfo) setHour(dst **int32, name string, h int32) *FlightInfo {
	if h < 0 || h > 23 {
		return fi.fail(fmt.Errorf("%s: %d is outside 0-23", name, h))
	}
	*dst = &h
	return fi
}

// MaxDuration caps the whole leg gate to gate. It is rounded down to the
// minute.
func (fi *FlightInfo) MaxDuration(d time.Duration) *FlightInfo {
	m, err := minutes("max duration", d)
	if err != nil {
		return fi.fail(err)
	}
	fi.maxDurationMinutes = &m
	return fi
}

// From sets the origin. Several may be given to search them at once, and
// metro codes such as NYC expand to every airport in the area.
func (fi *FlightInfo) From(codes ...string) *FlightInfo {
	if err := checkAirports("origin", codes); err != nil {
		return fi.fail(err)
	}
	fi.from = codes
	return fi
}

// To sets the destination, with the same rules as From.
func (fi *FlightInfo) To(codes ...string) *FlightInfo {
	if err := checkAirports("destination", codes); err != nil {
		return fi.fail(err)
	}
	fi.to = codes
	return fi
}

// ConnectingAirports restricts layovers to the given IATA codes. Nonstop
// flights are not excluded by this.
func (fi *FlightInfo) ConnectingAirports(codes ...string) *FlightInfo {
	if err := checkAirports("connecting airport", codes); err != nil {
		return fi.fail(err)
	}
	fi.connectingAirports = codes
	return fi
}

// MinLayover sets the shortest acceptable connection. Nonstop flights are not
// excluded by this.
func (fi *FlightInfo) MinLayover(d time.Duration) *FlightInfo {
	m, err := minutes("min layover", d)
	if err != nil {
		return fi.fail(err)
	}
	fi.minLayoverMinutes = &m
	return fi
}

// MaxLayover sets the longest acceptable connection. Nonstop flights are not
// excluded by this.
func (fi *FlightInfo) MaxLayover(d time.Duration) *FlightInfo {
	m, err := minutes("max layover", d)
	if err != nil {
		return fi.fail(err)
	}
	fi.maxLayoverMinutes = &m
	return fi
}

// LessEmissions filters for only itineraries Google marks as lower emission.
func (fi *FlightInfo) LessEmissions() *FlightInfo {
	fi.lessEmissions = true
	return fi
}

// build validates the leg and converts it to its wire form.
func (fi *FlightInfo) build() (*pb.FlightInfo, error) {
	if fi.err != nil {
		return nil, fi.err
	}
	if fi.date == "" {
		return nil, errors.New("required field date unset")
	}
	if len(fi.from) == 0 {
		return nil, errors.New("required field from unset")
	}
	if len(fi.to) == 0 {
		return nil, errors.New("required field to unset")
	}
	if err := checkHourRange(fi.earliestDepartureHour, fi.latestDepartureHour, "departure"); err != nil {
		return nil, err
	}
	if err := checkHourRange(fi.earliestArrivalHour, fi.latestArrivalHour, "arrival"); err != nil {
		return nil, err
	}
	if fi.minLayoverMinutes != nil && fi.maxLayoverMinutes != nil &&
		*fi.minLayoverMinutes > *fi.maxLayoverMinutes {
		return nil, errors.New("min layover is longer than max layover")
	}

	out := &pb.FlightInfo{
		Date:                  fi.date,
		MaxStops:              fi.maxStops,
		Airlines:              fi.airlines,
		EarliestDepartureHour: fi.earliestDepartureHour,
		LatestDepartureHour:   fi.latestDepartureHour,
		EarliestArrivalHour:   fi.earliestArrivalHour,
		LatestArrivalHour:     fi.latestArrivalHour,
		MaxDurationMinutes:    fi.maxDurationMinutes,
		From:                  airports(fi.from),
		To:                    airports(fi.to),
		ConnectingAirports:    fi.connectingAirports,
		MinLayoverMinutes:     fi.minLayoverMinutes,
		MaxLayoverMinutes:     fi.maxLayoverMinutes,
	}
	if fi.lessEmissions {
		out.Emissions = []pb.Emissions{pb.Emissions_EMISSIONS_LESS_ONLY}
	}

	for i, s := range fi.selectedSegments {
		seg, err := s.build()
		if err != nil {
			return nil, fmt.Errorf("selected segment %d: %w", i, err)
		}
		out.SelectedSegments = append(out.SelectedSegments, seg)
	}

	return out, nil
}

// Request is a whole search: one or more legs plus the party and fare
// preferences that apply across them.
type Request struct {
	flights []*FlightInfo

	passengers []Passenger
	class      Class

	// tripType is abstracted away through the separation of each tripType into
	// distinct execution functions, so no exported builder function exists. Field
	// is populated in execution functions.
	tripType pb.TripType

	maxPrice *int32

	carryOnBag *int32
	checkedBag *int32

	hideSeparateAndSelfTransfer *bool
	excludeBasicEconomy         *bool

	// Locale of the search. Empty means the default, applied when the URL is
	// rendered rather than when the field is set, so the zero value keeps
	// meaning "unset".
	currency string
	language string
	region   string

	// Stores first encountered error when building.
	err error
}

func NewRequest() *Request {
	return &Request{}
}

func (r *Request) fail(err error) *Request {
	if r.err == nil {
		r.err = err
	}
	return r
}

// Flights sets the legs of the trip, in travel order.
func (r *Request) Flights(flights ...*FlightInfo) *Request {
	r.flights = flights
	return r
}

// AddFlight appends one leg, for building a trip up a leg at a time.
func (r *Request) AddFlight(f *FlightInfo) *Request {
	r.flights = append(r.flights, f)
	return r
}

// Passengers sets the travelling party. Left empty, Google prices for a single
// adult.
func (r *Request) Passengers(p ...Passenger) *Request {
	r.passengers = p
	return r
}

// Adults adds n adults to the party.
func (r *Request) Adults(n int) *Request {
	return r.add(PassengerAdult, n, "adults")
}

// Children adds n children to the party.
func (r *Request) Children(n int) *Request {
	return r.add(PassengerChild, n, "children")
}

// InfantsInSeat adds n infants occupying their own seat.
func (r *Request) InfantsInSeat(n int) *Request {
	return r.add(PassengerInfantInSeat, n, "infants in seat")
}

// InfantsOnLap adds n infants travelling on a lap.
func (r *Request) InfantsOnLap(n int) *Request {
	return r.add(PassengerInfantOnLap, n, "infants on lap")
}

func (r *Request) add(p Passenger, n int, name string) *Request {
	if n < 0 {
		return r.fail(fmt.Errorf("%s: %d is negative", name, n))
	}
	for range n {
		r.passengers = append(r.passengers, p)
	}

	return r
}

// Class sets the cabin to search. Google flights defaults unset to economy.
func (r *Request) Class(c Class) *Request {
	r.class = c
	return r
}

// TripType sets the shape of the trip, which is then checked against the
// number of legs when the request is built. TripTypeUnspecified is valid and
// is the default: it leaves the field off the request and lets the server
// infer the trip.
//
// TripTypeMultiCity is accepted but does not yet produce results. Google omits
// itineraries from the page for any trip that is neither a one way nor a round
// trip, so such a search returns an empty slice rather than an error. This is
// a limitation of the package, not of the argument.
func (r *Request) TripType(t TripType) *Request {
	switch t {
	case TripTypeUnspecified, TripTypeOneWay, TripTypeRoundTrip, TripTypeMultiCity:
	default:
		return r.fail(fmt.Errorf("trip type: %d is not a known value", t))
	}
	r.tripType = pb.TripType(t)
	return r
}

// MaxPrice caps the fare in whole currency units, matched against the currency
// the search runs in.
func (r *Request) MaxPrice(n int32) *Request {
	if n <= 0 {
		return r.fail(fmt.Errorf("max price: %d is not positive", n))
	}
	r.maxPrice = &n
	return r
}

// Currency sets the ISO 4217 code fares are quoted in. Left unset, the
// parameter is omitted and Google picks one from where the search runs from,
// so anything needing a predictable currency has to set it.
//
// This selects results rather than just presenting them: prices are quoted per
// market, and FlightOption.DecimalDigits moves with the currency, since a
// currency without minor units reports zero. MaxPrice is compared against this
// currency too.
func (r *Request) Currency(code string) *Request {
	if len(code) != 3 || !isAlpha(code) {
		return r.fail(fmt.Errorf("currency %q: not a three-letter ISO 4217 code", code))
	}
	r.currency = strings.ToUpper(code)
	return r
}

// Language sets the ISO 639-1 code the page is rendered in. Left unset, the
// parameter is omitted.
//
// Results are decoded from the booking tokens rather than the rendered text,
// so this has little effect today. Setting it pins the payload so it cannot
// shift under the decoder if Google varies the page by locale.
func (r *Request) Language(code string) *Request {
	if len(code) != 2 || !isAlpha(code) {
		return r.fail(fmt.Errorf("language %q: not a two-letter ISO 639-1 code", code))
	}
	r.language = strings.ToLower(code)
	return r
}

// Region sets the ISO 3166-1 alpha-2 country the search runs from, affecting
// which markets and fares are offered. Left unset, the parameter is omitted
// and Google infers it.
func (r *Request) Region(code string) *Request {
	if len(code) != 2 || !isAlpha(code) {
		return r.fail(fmt.Errorf("region %q: not a two-letter ISO 3166-1 code", code))
	}
	r.region = strings.ToLower(code)
	return r
}

// CarryOnBag keeps only fares that include a carry-on. It filters rather than
// adds a bag, so prices shift to fares that already allow one.
func (r *Request) CarryOnBag(n int32) *Request {
	r.carryOnBag = &n
	return r
}

// CheckedBag keeps only fares that include a checked bag, with the same
// filtering behaviour as CarryOnBag.
func (r *Request) CheckedBag(n int32) *Request {
	r.checkedBag = &n
	return r
}

// ExcludeBasicEconomy drops basic economy fares.
func (r *Request) ExcludeBasicEconomy() *Request {
	t := true
	r.excludeBasicEconomy = &t
	return r
}

// HideSeparateAndSelfTransfer drops itineraries booked as separate tickets or
// requiring the traveller to move their own bags between flights.
//
// Unverified: no route probed produced such an itinerary, so this has never
// been observed to change a result.
func (r *Request) HideSeparateAndSelfTransfer() *Request {
	t := true
	r.hideSeparateAndSelfTransfer = &t
	return r
}

// build validates the request and converts it to its wire form.
func (r *Request) build() (*pb.Request, error) {
	if r == nil {
		return nil, errors.New("nil request")
	}
	if r.err != nil {
		return nil, r.err
	}
	if len(r.flights) == 0 {
		return nil, errors.New("no flights added to request")
	}
	switch r.tripType {
	case pb.TripType_TRIP_TYPE_ONE_WAY:
		if len(r.flights) != 1 {
			return nil, errors.New("expected one FlightInfos for one way request")
		}
	case pb.TripType_TRIP_TYPE_ROUND_TRIP:
		if len(r.flights) != 2 {
			return nil, errors.New("expected two FlightInfos for round trip request")
		}
	}

	out := &pb.Request{
		Class:                       pb.Class(r.class),
		MaxPrice:                    r.maxPrice,
		HideSeparateAndSelfTransfer: r.hideSeparateAndSelfTransfer,
		TripType:                    r.tripType,
		ExcludeBasicEconomy:         r.excludeBasicEconomy,
	}

	for i, f := range r.flights {
		if f == nil {
			return nil, fmt.Errorf("flight %d: nil", i)
		}
		leg, err := f.build()
		if err != nil {
			return nil, fmt.Errorf("flight %d: %w", i, err)
		}
		out.Flights = append(out.Flights, leg)
	}

	if len(r.passengers) > 9 {
		return nil, errors.New("Google flights doesn't support more than 9 passengers")
	}
	nAdults := 0
	nInfantsOnLap := 0
	nPassengersInSeats := int32(0)
	for _, p := range r.passengers {
		if p != PassengerInfantOnLap {
			nPassengersInSeats++
		}
		if p == PassengerAdult {
			nAdults++
		}
		if p == PassengerInfantOnLap {
			nInfantsOnLap++
		}
		out.Passengers = append(out.Passengers, pb.Passenger(p))
	}
	if nAdults == 0 {
		return nil, errors.New("zero adult passengers added to flight")
	}
	// TODO: check if this is necessary. If nInfantsOnLap > nAdults but price still
	// increases, then check is unnecessary.
	// if nInfantsOnLap > nAdults {
	// 	return nil, errors.New("number of infants on lap cannot exceed number of adults")
	// }

	if r.carryOnBag != nil && *r.carryOnBag > nPassengersInSeats {
		return nil, errors.New("number of carry on bags cannot exceed number of passengers in seats")
	}
	if r.checkedBag != nil && *r.checkedBag > nPassengersInSeats {
		return nil, errors.New("number of checked bags cannot exceed number of passengers in seats")
	}
	if r.carryOnBag != nil || r.checkedBag != nil {
		out.Baggage = &pb.Baggage{CarryOnBag: r.carryOnBag, CheckedBag: r.checkedBag}
	}
	return out, nil
}

func (r *Request) URL() (*url.URL, error) {
	msg, err := r.build()
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	tfs, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}

	// Execute renders its URL through here, so the locale reaches both entry
	// points from this one place.
	//
	// Encode leaves the token alone: base64url's alphabet is entirely
	// unreserved in a query string.
	q := url.Values{"tfs": {base64.RawURLEncoding.EncodeToString(tfs)}}

	// An unset locale is left off the URL rather than defaulted, so Google
	// applies its own. That makes results depend on where the search runs
	// from, which is why anything needing a stable currency has to say so.
	for param, value := range map[string]string{
		"hl":   r.language,
		"gl":   r.region,
		"curr": r.currency,
	} {
		if value != "" {
			q.Set(param, value)
		}
	}
	u.RawQuery = q.Encode()

	return u, nil
}

// Execute runs the search and returns the itineraries on the first page of
// results, in the order Google ranked them, using http.DefaultClient.
//
// It returns ErrNoFlights when the search matched nothing, and
// ErrPartialResults — alongside the itineraries it did get — when Google
// withheld the rest.
func (r *Request) Execute(ctx context.Context) ([]FlightOption, error) {
	return search(ctx, r, http.DefaultClient, nil)
}

// ExecuteWith is Execute run against the given client and headers, for callers
// who need their own timeout, transport, proxy, instrumentation or identity.
// Both arguments are optional: a nil client falls back to http.DefaultClient,
// and a nil header sends only the defaults.
//
// Entries in header replace the default of the same name rather than adding to
// it, and any other entry is sent as well. Overriding User-Agent is the usual
// reason to pass one.
func (r *Request) ExecuteWith(ctx context.Context, client *http.Client, header http.Header) ([]FlightOption, error) {
	return search(ctx, r, client, header)
}

func airports(codes []string) []*pb.Airport {
	out := make([]*pb.Airport, 0, len(codes))
	for _, c := range codes {
		out = append(out, &pb.Airport{Code: c})
	}
	return out
}

func checkAirports(name string, codes []string) error {
	if len(codes) == 0 {
		return fmt.Errorf("%s: none given", name)
	}
	for _, c := range codes {
		if len(c) != 3 {
			return fmt.Errorf("%s %q: not a three-letter code", name, c)
		}
	}
	return nil
}

func checkHourRange(earliest, latest *int32, name string) error {
	if earliest != nil && latest != nil && *earliest > *latest {
		return fmt.Errorf("earliest %s hour %d is after latest %d", name, *earliest, *latest)
	}
	return nil
}

func minutes(name string, d time.Duration) (int32, error) {
	if d <= 0 {
		return 0, fmt.Errorf("%s: %s is not positive", name, d)
	}
	return int32(d.Minutes()), nil
}

// isAlpha reports whether s is all ASCII letters, so a locale code cannot
// smuggle a separator or an escape into the query string.
func isAlpha(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}
