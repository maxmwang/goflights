package goflights

import (
	"errors"
	"fmt"
	"time"

	"github.com/maxmwang/goflights/internal/pb"
)

var (
	classFromProtoToString = map[pb.Class]string{
		pb.Class_CLASS_UNSPECIFIED:     "Unspecified",
		pb.Class_CLASS_ECONOMY:         "Economy",
		pb.Class_CLASS_PREMIUM_ECONOMY: "Premium Economy",
		pb.Class_CLASS_BUSINESS:        "Business",
		pb.Class_CLASS_FIRST:           "First",
	}
)

// FlightOption is one itinerary returned by a search.
type FlightOption struct {
	// Decimal places for fare amounts. For example, 2 for USD, 0 for JPY.
	DecimalDigits int

	// Amount in the currency's minor units, scaled by DecimalDigits, so
	// 19920 with 2 digits is 199.20. Covers the whole party, not one
	// passenger.
	Price    int64
	Currency string

	Segments []FlightSegment
}

// FlightSegment is one flight within an itinerary, reaching
// FlightInfo.SelectOption as part of a chosen one.
//
// Segments are normally taken straight from a search rather than assembled.
// One built by hand is checked field by field before the request goes out,
// because a selection with a field missing is silently ignored by Google,
// which returns the original leg rather than an error. A selection that is
// well formed but matches no real itinerary is safe by comparison: it simply
// comes back empty.
type FlightSegment struct {
	// Airports are IATA codes
	FromAirport string
	ToAirport   string

	// Times carry the airport's UTC offset, so their dates are local and the
	// arrival may fall on the following day.
	DepartureTime time.Time
	ArrivalTime   time.Time

	Airline      string
	FlightNumber string

	// TicketingAirline matched Airline in every response observed, including
	// true codeshares. It is not the operating airline, which the booking
	// token does not carry.
	TicketingAirline      string
	TicketingFlightNumber string

	// Class is the cabin for this segment. A single itinerary may mix cabins.
	Class string

	// IATA aircraft type code, e.g. "321". The JSON spells it "Airbus A321".
	Aircraft string
}

// validate reports whether the segment can be used as a selection. A selection
// with a missing field is ignored by Google rather than reported, coming back
// as the original leg, so every field it carries is checked before sending.
func (fs FlightSegment) validate() error {
	for _, f := range []struct{ name, value string }{
		{"from airport", fs.FromAirport},
		{"to airport", fs.ToAirport},
		{"airline", fs.Airline},
		{"flight number", fs.FlightNumber},
	} {
		if f.value == "" {
			return fmt.Errorf("no %s", f.name)
		}
	}
	if err := checkAirports("from airport", []string{fs.FromAirport}); err != nil {
		return err
	}
	if err := checkAirports("to airport", []string{fs.ToAirport}); err != nil {
		return err
	}
	if fs.FromAirport == fs.ToAirport {
		return fmt.Errorf("from and to airport are both %q", fs.FromAirport)
	}

	// A zero time formats to a well-formed "0001-01-01", so it has to be
	// caught here rather than by the date check on the built selection.
	if fs.DepartureTime.IsZero() {
		return errors.New("departure time is unset")
	}
	if fs.ArrivalTime.IsZero() {
		return errors.New("arrival time is unset")
	}
	if !fs.ArrivalTime.After(fs.DepartureTime) {
		return fmt.Errorf("arrival %s is not after departure %s",
			fs.ArrivalTime.Format(time.RFC3339), fs.DepartureTime.Format(time.RFC3339))
	}
	return nil
}

func (fs FlightSegment) toSelectedSegment() selectedSegment {
	return selectedSegment{
		fromAirport: fs.FromAirport,
		toAirport:   fs.ToAirport,

		date: fs.DepartureTime.Format(time.DateOnly),

		airline:      fs.Airline,
		flightNumber: fs.FlightNumber,
	}
}

func fromProto(bt *pb.BookingToken) (FlightOption, error) {
	// TODO: should rethink whether it is necessary to check if any fields are
	// unexpectedly missing. Get methods will return the zero value if field is
	// unset, which is not necessarily correct.
	foZero := FlightOption{}

	fare := bt.GetFare()
	if fare == nil {
		return foZero, errors.New("unexpected nil fare")
	}
	trip := bt.GetTrip()
	if trip == nil {
		return foZero, errors.New("unexpected nil trip")
	}
	tripSlices := trip.GetSlices()
	if len(tripSlices) == 0 {
		return foZero, errors.New("unexpected nil slices")
	}
	// We expect only ever one slice per response. For a round trip search returns
	// tokens for the outbound leg, and the return is chosen against a follow-up
	// request.
	//
	// If we do ever encounter multiple slices, then the following code is not
	// necessarily correct, so we return an error instead.
	if len(tripSlices) > 1 {
		return foZero, errors.New("unexpected multiple slices, expect only one per response")
	}
	slice := tripSlices[0]

	segments := slice.GetSegments()
	if len(segments) == 0 {
		return foZero, errors.New("unexpected nil segments")
	}

	fo := FlightOption{
		// if DecimalDigits is unset, default to 0. Unsure if this is correct
		DecimalDigits: int(bt.GetDecimalDigits()),
		Price:         fare.GetAmount(),
		Currency:      bt.GetCurrency(),

		Segments: make([]FlightSegment, 0, len(segments)),
	}

	for i, s := range segments {
		departureTime, err := time.Parse(time.RFC3339, s.GetDepartureTime())
		if err != nil {
			return foZero, fmt.Errorf("segment %d: departure time parse: %w", i, err)
		}
		if departureTime.IsZero() {
			return foZero, fmt.Errorf("segment %d: departure time is unset", i)
		}
		arrivalTime, err := time.Parse(time.RFC3339, s.GetArrivalTime())
		if err != nil {
			return foZero, fmt.Errorf("segment %d: arrival time parse: %w", i, err)
		}
		if arrivalTime.IsZero() {
			return foZero, fmt.Errorf("segment %d: arrival time is unset", i)
		}

		fs := FlightSegment{
			FromAirport: s.GetFromAirport(),
			ToAirport:   s.GetToAirport(),

			DepartureTime: departureTime,
			ArrivalTime:   arrivalTime,

			Airline:               s.GetAirline(),
			FlightNumber:          s.GetFlightNumber(),
			TicketingAirline:      s.GetTicketingAirline(),
			TicketingFlightNumber: s.GetTicketingFlightNumber(),

			Class: classFromProtoToString[s.GetClass()],

			Aircraft: s.GetAircraft(),
		}

		fo.Segments = append(fo.Segments, fs)
	}

	return fo, nil
}
