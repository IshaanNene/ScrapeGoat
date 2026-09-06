package provenance

import (
	"strconv"
	"strings"
	"time"
)

// FreshUntil reports when a record stops being fresh by the server's own
// instruction, and whether the server gave one at all.
//
// This is the difference between a cheap refresh and a free one. A validator
// answers "has this changed?" and costs a request to ask; Cache-Control answers
// "is it worth asking yet?", and a page the server said is good for an hour does
// not need revalidating twice within it.
//
// Only max-age is read. Expires is the older mechanism and is deliberately
// ignored: it is an absolute HTTP-date, so honouring it means trusting two clocks
// to agree, and being wrong in that direction means skipping a page that has
// actually changed. Not reading it costs a conditional request, which is the safe
// way to be wrong.
//
// no-store and no-cache both defeat this entirely, and must. no-cache does not
// mean "do not store", it means "do not reuse without revalidating" — exactly the
// thing being decided here.
func (r Record) FreshUntil() (time.Time, bool) {
	if r.FetchedAt.IsZero() || r.CacheControl == "" {
		return time.Time{}, false
	}

	maxAge, ok := maxAgeOf(r.CacheControl)
	if !ok {
		return time.Time{}, false
	}

	// A response relayed by an upstream cache arrived already partly used up, so
	// its remaining life is shorter than its max-age by however long it had
	// already been held. Ignoring Age would treat a response that spent most of
	// its life in a CDN as though it had just been generated.
	lifetime := maxAge - ageOf(r.Age)
	if lifetime <= 0 {
		return time.Time{}, false
	}
	return r.FetchedAt.Add(lifetime), true
}

// IsFresh reports whether the server's directive still covers this record at now.
func (r Record) IsFresh(now time.Time) bool {
	until, ok := r.FreshUntil()
	return ok && now.Before(until)
}

// maxAgeOf extracts max-age from a Cache-Control header.
//
// Returns false for no-store and no-cache regardless of any max-age alongside
// them, because those directives are about reuse and this function's answer is
// used to decide whether to reuse.
func maxAgeOf(header string) (time.Duration, bool) {
	var maxAge time.Duration
	found := false

	for _, part := range strings.Split(header, ",") {
		directive := strings.ToLower(strings.TrimSpace(part))
		switch {
		case directive == "no-store", directive == "no-cache":
			return 0, false
		case strings.HasPrefix(directive, "no-cache="):
			// Field-scoped no-cache applies to named headers, not the body, so it
			// does not forbid reusing the representation. Treated as permissive
			// rather than guessing, since the alternative is a needless refetch.
			continue
		case strings.HasPrefix(directive, "max-age="):
			secs, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(directive, "max-age=")))
			if err != nil || secs < 0 {
				continue
			}
			maxAge = time.Duration(secs) * time.Second
			found = true
		}
	}
	return maxAge, found
}

// ageOf parses the Age header, which is a count of seconds. An unparseable or
// absent value is treated as zero: it makes the record look fresher than it may
// be, so it is the direction that costs a request rather than skips one — and a
// malformed Age is a server bug, not a licence to guess high.
func ageOf(header string) time.Duration {
	if header == "" {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
