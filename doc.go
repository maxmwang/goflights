// Package goflights is an unofficial client built on the same request Google
// Flights itself makes: the search travels as a protobuf message in the tfs
// query parameter of a plain GET, and every itinerary on the resulting page
// carries a bookingtoken that is itself protobuf. Results are decoded from
// those tokens rather than scraped, which is what makes exact fares, flight
// numbers and timestamps with UTC offsets available. Nothing here is a
// supported API, so it can break whenever Google changes the format.
//
// # Searching
//
// A search is a [Request] holding one [FlightInfo] per leg. Setters chain, and
// the first error is held until the request is built, so a chain never has to
// be broken up to check errors:
//
//	req := goflights.NewRequest().
//		Adults(1).
//		Currency("USD").
//		Flights(
//			goflights.NewFlightInfo().
//				DepartureDateStr("2026-09-01").
//				From("SFO").
//				To("JFK"),
//		)
//
//	options, err := req.Execute(ctx)
//	if err != nil && !errors.Is(err, goflights.ErrPartialResults) {
//		return err
//	}
//	for _, o := range options {
//		fmt.Println(o.Currency, o.Price, o.Segments[0].Airline)
//	}
//
// [Request.URL] renders the same search as a URL without running it, which is
// also the page a person can open in a browser.
//
// # Results
//
// [FlightOption.Price] is in the currency's minor units and has to be scaled
// by [FlightOption.DecimalDigits], for example 2 for USD but 0 for JPY:
//
//	amount := float64(o.Price) / math.Pow10(o.DecimalDigits)
//
// # Round trips
//
// A round trip is two legs, and a search returns options for the first one
// only. Pinning a chosen itinerary with [FlightInfo.SelectOption] makes the
// next search return options for the leg after it:
//
//	outbound := goflights.NewFlightInfo().DepartureDateStr("2026-09-01").From("SFO").To("JFK")
//	inbound := goflights.NewFlightInfo().DepartureDateStr("2026-09-08").From("JFK").To("SFO")
//	req := goflights.NewRequest().Adults(1).Flights(outbound, inbound)
//
//	departures, err := req.Execute(ctx)
//	// ...
//	outbound.SelectOption(departures[0])
//	returns, err := req.Execute(ctx)
//
// # Multi City
//
// Currently not supported.
//
// # Errors
//
// [ErrNoFlights] means the search matched nothing, or was rejected outright —
// a date in the past reports the same way.
//
// [ErrPartialResults] is returned alongside usable itineraries. Google
// server-renders the full list only for parties of four or fewer with no
// infants; beyond that it renders one itinerary and loads the rest over a
// follow-up request this package does not implement. Without the error that
// shortfall is invisible, so treat it as a warning rather than a failure.
//
// # Locale
//
// [Request.Currency], [Request.Language] and [Request.Region] are omitted from
// the search when unset, leaving Google to infer them from where the search
// runs.
package goflights
