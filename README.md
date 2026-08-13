# goflights

[![Go Reference](https://pkg.go.dev/badge/github.com/maxmwang/goflights.svg)](https://pkg.go.dev/github.com/maxmwang/goflights)

An unofficial Go client for Google Flights. No API key, no browser, no HTML
scraping.

```bash
go get github.com/maxmwang/goflights
```

## Usage

```go
req := goflights.NewRequest().
    Adults(1).
    Currency("USD").
    Flights(
        goflights.NewFlightInfo().
            DepartureDateStr("2026-09-01").
            From("SFO").
            To("JFK"),
    )

options, err := req.Execute(ctx)
if err != nil && !errors.Is(err, goflights.ErrPartialResults) {
    return err
}

for _, o := range options {
    s := o.Segments[0]
    fmt.Printf("%s %d  %s%s  %s -> %s\n",
        o.Currency, o.Price, s.Airline, s.FlightNumber, s.FromAirport, s.ToAirport)
}
```

`Price` is in the currency's minor units and must be scaled by `DecimalDigits`
— 2 for USD, but **0 for JPY**:

```go
amount := float64(o.Price) / math.Pow10(o.DecimalDigits)
```

`Request.URL()` renders the same search as a URL without running it, which is
also the page you can open in a browser.

### Round trips

A round trip is two legs, and a search returns options for the first one only.
Pinning a chosen itinerary makes the next search return options for the leg
after it:

```go
outbound := goflights.NewFlightInfo().DepartureDateStr("2026-09-01").From("SFO").To("JFK")
inbound := goflights.NewFlightInfo().DepartureDateStr("2026-09-08").From("JFK").To("SFO")
req := goflights.NewRequest().Adults(1).Flights(outbound, inbound)

departures, err := req.Execute(ctx)
// ...
outbound.SelectOption(departures[0])
returns, err := req.Execute(ctx)
```

### Filters

Per leg, on `FlightInfo`:

| Method | Effect |
| --- | --- |
| `MaxStops(n)` | Caps connections. `0` is nonstop only. |
| `Airlines(codes...)` | Restricts the leg to those IATA airline codes. |
| `ConnectingAirports(codes...)` | Restricts layovers to those airports. Nonstops are not excluded. |
| `MinLayover(d)` / `MaxLayover(d)` | Bounds a connection's length. Nonstops are not excluded. |
| `MaxDuration(d)` | Caps the whole leg gate to gate, not a single segment. |
| `EarliestDepartureHour(h)` / `LatestDepartureHour(h)` | Local hour, `0`–`23`, inclusive. |
| `EarliestArrivalHour(h)` / `LatestArrivalHour(h)` | Same, on arrival. A low latest arrival selects red-eyes. |
| `LessEmissions()` | Keeps only itineraries Google marks as lower emission. |

Per search, on `Request`:

| Method | Effect |
| --- | --- |
| `Adults(n)` / `Children(n)` / `InfantsInSeat(n)` / `InfantsOnLap(n)` | Builds the party. Nine passengers is the maximum. |
| `Passengers(p...)` | Sets the party directly, instead of the counters above. |
| `Class(c)` | Cabin to search. Results may still mix cabins across segments. |
| `MaxPrice(n)` | Whole currency units, in the currency the search runs in. |
| `CarryOnBag(n)` / `CheckedBag(n)` | Keeps only fares including that many bags. Filters rather than adds, so prices rise. |
| `ExcludeBasicEconomy()` | Drops basic economy fares. |
| `HideSeparateAndSelfTransfer()` | Unverified — no route probed produced such an itinerary. |
| `Currency(code)` / `Language(code)` / `Region(code)` | Omitted when unset, leaving Google to infer them. |
| `TripType(t)` | Checks the leg count against the trip. Multi city is not supported. |

## How it works

Both directions are protobuf. The search travels as a protobuf message in the
`tfs` query parameter of a plain GET, and every itinerary on the resulting page
carries a booking token that is itself protobuf. Results are decoded from those
tokens rather than scraped from the rendered HTML.

That is what makes exact values available. The page's own JSON rounds a fare to
`411` and reports times as `[23, 28]` with the date in a separate field; the
token holds `41100` — $411.00 — alongside `2026-09-01T23:28:00-07:00`, so an
overnight flight and a timezone change are both self-evident. Flight numbers
appear only in the token.

The request and response schemas were reverse engineered and are documented
field by field in [`internal/pb`](internal/pb), including how each was
verified.

## Limitations

- **Multi city is not supported.** Searches whose legs do not form a one way or
  a round trip come back empty. The trip type is not the cause — Google leaves
  those itineraries out of the page entirely.
- **Large parties return partial results.** Google renders the full list only
  for parties of four or fewer with no infants. Beyond that it renders one
  itinerary and defers the rest, which `Execute` reports as
  `ErrPartialResults` alongside whatever it did get. The itineraries returned
  are real; there are simply more.
- **No booking.** This searches; it does not book, hold, or price a
  reservation.

## Stability

Nothing here is a supported API. Google can change the format at any time and
this will break. The schemas were verified against live responses, but that is
a snapshot, not a contract. Pin a version.

## License

[MIT](LICENSE)
