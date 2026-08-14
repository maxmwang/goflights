package goflights

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
)

const (
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

	// maxBody caps the read; result pages run a few MB.
	maxBody = 16 << 20
)

// defaultHeader is what every search sends unless the caller overrides it.
//
// The User-Agent is not decoration. Google returns a page with no results
// block unless it recognises the value. Measured against live responses: full
// browser strings and "curl/8.0" work, while "Go-http-client/1.1", an empty
// value, a bare "Mozilla/5.0" and invented tokens such as "my-app/1.0" all
// come back empty. A replacement has to be a string Google already knows.
func defaultHeader() http.Header {
	h := make(http.Header, 2)
	h.Set("User-Agent", userAgent)
	h.Set("Accept-Language", "en-US,en;q=0.9")
	return h
}

// ErrPartialResults reports that Google server-rendered only a single
// representative itinerary and deferred the rest to a follow-up RPC this
// package does not implement.
//
// The itineraries returned alongside it are real and correctly decoded; there
// are simply more that a plain GET cannot reach. Without this the shortfall is
// invisible — a party of five gets one itinerary and no indication that thirty
// were available.
var ErrPartialResults = errors.New("partial results: Google deferred the rest to a follow-up request")

// partyRenderLimit is the party size at which Google stops server-rendering
// the full list. Measured: four passengers of any mix return the whole list,
// five return one.
const partyRenderLimit = 5

// resultsDeferred reports whether Google will withhold most of the results for
// this party.
//
// The condition is a property of the request, not the response: a truncated
// page is structurally indistinguishable from the reply to a leg selection,
// which is also missing its best-flights section. Two triggers were found, and
// either alone is enough — a party of five or more, or the presence of an
// infant of either kind, even in a party of two.
func (r *Request) resultsDeferred() bool {
	if len(r.passengers) >= partyRenderLimit {
		return true
	}
	for _, p := range r.passengers {
		if p == PassengerInfantInSeat || p == PassengerInfantOnLap {
			return true
		}
	}
	return false
}

// search runs req and returns the itineraries on the first page of results, in the
// order Google ranked them. It returns ErrNoFlights when the search matched
// nothing, and ErrPartialResults — alongside the itineraries it did get — when
// Google withheld the rest.
func search(ctx context.Context, req *Request, client *http.Client, header http.Header) ([]FlightOption, error) {
	if client == nil {
		client = http.DefaultClient
	}

	u, err := req.URL()
	if err != nil {
		return nil, err
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	// Caller entries replace the defaults outright rather than adding to
	// them, so overriding User-Agent yields one value and not two.
	hreq.Header = defaultHeader()
	for name, values := range header {
		hreq.Header[http.CanonicalHeaderKey(name)] = slices.Clone(values)
	}

	resp, err := client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	itineraries, err := decode(string(body))
	if err != nil {
		return nil, err
	}
	res := make([]FlightOption, 0, len(itineraries))
	for _, it := range itineraries {
		itProto, err := fromProto(it)
		if err != nil {
			// TODO: return successfully parsed itineraries and a new error denoting
			// partial failure
			return nil, fmt.Errorf("parse proto: %w", err)
		}
		res = append(res, itProto)
	}

	if req.resultsDeferred() {
		return res, ErrPartialResults
	}
	return res, nil
}
