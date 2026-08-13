// Primarily for local development testing
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/maxmwang/goflights"
)

func main() {
	reqs := []*goflights.Request{
		goflights.NewRequest().
			Adults(1).
			Flights(
				goflights.NewFlightInfo().
					DepartureDateStr("2026-09-01").
					From("SFO").
					To("JFK"),
			),
		goflights.NewRequest().
			Adults(1).
			InfantsOnLap(1).
			Flights(
				goflights.NewFlightInfo().
					DepartureDateStr("2026-09-01").
					From("SFO").
					To("JFK"),
			),
		goflights.NewRequest().
			Adults(1).
			InfantsOnLap(2).
			Flights(
				goflights.NewFlightInfo().
					DepartureDateStr("2026-09-01").
					From("SFO").
					To("JFK"),
			),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, req := range reqs {
		options, err := req.Execute(ctx)
		// Partial results still carry usable itineraries, so report and go on.
		if errors.Is(err, goflights.ErrPartialResults) {
			fmt.Printf("warning: %v\n", err)
		} else if err != nil {
			log.Fatal(err)
		}

		if len(options) == 1 {
			print(options)
		}

		p := getPricesOnlyAndSort(options)
		fmt.Printf("%v\n", p)
	}
}

func getPricesOnlyAndSort(opts []goflights.FlightOption) []float64 {
	o := make([]float64, 0, len(opts))
	for _, opt := range opts {
		o = append(o, price(opt))
	}
	slices.Sort(o)

	return o
}

func print(opts []goflights.FlightOption) {
	for _, opt := range opts {
		fmt.Printf("%8s  %-24s  %s\n", fare(opt), strings.Join(airlines(opt), "/"), route(opt))
	}
}

// price scales the amount out of the currency's minor units.
func price(opt goflights.FlightOption) float64 {
	amount := float64(opt.Price)
	for range opt.DecimalDigits {
		amount /= 10
	}
	return amount
}

// fare renders the amount using the option's own scale, so a currency without
// minor units prints without a decimal point.
func fare(opt goflights.FlightOption) string {
	return fmt.Sprintf("%s %.*f", opt.Currency, opt.DecimalDigits, price(opt))
}

// airlines lists the distinct airlines, in segment order. The booking token
// also carries display names, but FlightOption keeps only the codes.
func airlines(opt goflights.FlightOption) []string {
	var out []string
	for _, s := range opt.Segments {
		if !slices.Contains(out, s.Airline) {
			out = append(out, s.Airline)
		}
	}
	return out
}

// route renders the segments as a single hop chain with departure and arrival
// times, e.g. "SFO 11:00 > DFW > JFK 22:14 (AA1849, AA1248)".
func route(opt goflights.FlightOption) string {
	segs := opt.Segments
	if len(segs) == 0 {
		return ""
	}

	hops := []string{segs[0].FromAirport}
	flights := make([]string, 0, len(segs))
	for _, s := range segs {
		hops = append(hops, s.ToAirport)
		flights = append(flights, s.Airline+s.FlightNumber)
	}

	return fmt.Sprintf("%s %s > %s (%s)",
		segs[0].DepartureTime.Format(time.DateTime),
		strings.Join(hops, " > "),
		segs[len(segs)-1].ArrivalTime.Format(time.DateTime),
		strings.Join(flights, ", "))
}
