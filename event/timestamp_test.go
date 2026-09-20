package event_test

// Event.Timestamp survives into DecisionRecord, the public projection a
// consumer marshals. time.Time.MarshalJSON refuses values RFC 3339 cannot
// express, so without a check at the input those values produce a successful
// Analyze whose record cannot be serialized — a failure surfacing far from
// the event that caused it, in code with no way to reject it.
//
// The rule here is deliberately the standard library's, not a stricter one
// of Trustvian's own invention. TestValidateMatchesStdlibMarshalling is what
// keeps that true: it asserts the verdicts agree case by case, so a
// divergence fails here rather than silently narrowing what callers may send.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
)

func at(t time.Time) event.Event {
	e := validEvent()
	e.Timestamp = t
	return e
}

// timestampCases are the boundaries of what RFC 3339 can express. Each is
// constructible as a time.Time; that is the point — Go lets you build these,
// and only marshalling complains.
var timestampCases = []struct {
	name string
	ts   time.Time
	ok   bool
}{
	{"normal UTC", time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), true},
	{"positive offset", time.Date(2026, 9, 20, 12, 0, 0, 0, time.FixedZone("+05:30", 5*3600+1800)), true},
	{"negative offset", time.Date(2026, 9, 20, 12, 0, 0, 0, time.FixedZone("-08:00", -8*3600)), true},
	{"maximum offset", time.Date(2026, 9, 20, 12, 0, 0, 0, time.FixedZone("+23:59", 23*3600+3540)), true},
	{"minimum offset", time.Date(2026, 9, 20, 12, 0, 0, 0, time.FixedZone("-23:59", -(23*3600+3540))), true},

	{"lowest year", time.Date(0, 1, 1, 0, 0, 0, 1, time.UTC), true},
	{"highest year", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), true},
	{"highest year in a western zone", time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("-12:00", -12*3600)), true},

	{"year below range", time.Date(-1, 12, 31, 0, 0, 0, 0, time.UTC), false},
	{"year above range", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
	{"offset of exactly 24 hours", time.Date(2026, 9, 20, 12, 0, 0, 0, time.FixedZone("+24:00", 24*3600)), false},
	{"offset beyond 24 hours west", time.Date(2026, 9, 20, 12, 0, 0, 0, time.FixedZone("-25:00", -25*3600)), false},
}

// TestValidateMatchesStdlibMarshalling is the contract: Validate accepts a
// timestamp if and only if encoding/json can encode it. Deriving the
// expectation from the standard library rather than restating it means this
// test also detects the reverse failure — a rule stricter than Go's, which
// would reject events nothing else objects to.
func TestValidateMatchesStdlibMarshalling(t *testing.T) {
	for _, tc := range timestampCases {
		t.Run(tc.name, func(t *testing.T) {
			_, marshalErr := tc.ts.MarshalJSON()
			if (marshalErr == nil) != tc.ok {
				t.Fatalf("the case table disagrees with the standard library: MarshalJSON() error = %v, want ok=%v", marshalErr, tc.ok)
			}

			err := at(tc.ts).Validate()
			switch {
			case tc.ok && err != nil:
				t.Fatalf("Validate() = %v, want nil — stdlib marshals this fine", err)
			case !tc.ok && !errors.Is(err, event.ErrInvalidTimestamp):
				t.Fatalf("Validate() = %v, want ErrInvalidTimestamp — stdlib refuses this (%v)", err, marshalErr)
			}
		})
	}
}

// TestZeroTimestampKeepsItsOwnError: the zero time is a valid RFC 3339 value
// (year 1, UTC), so only the earlier IsZero check distinguishes "not set"
// from "not encodable". Callers already match on ErrMissingTimestamp.
func TestZeroTimestampKeepsItsOwnError(t *testing.T) {
	var zero time.Time
	if _, err := zero.MarshalJSON(); err != nil {
		t.Fatalf("premise broken: the zero time no longer marshals (%v)", err)
	}

	err := at(zero).Validate()
	if !errors.Is(err, event.ErrMissingTimestamp) {
		t.Fatalf("Validate() = %v, want ErrMissingTimestamp", err)
	}
	if errors.Is(err, event.ErrInvalidTimestamp) {
		t.Fatal("the zero timestamp reported as unencodable rather than as absent")
	}
}

// TestInvalidTimestampErrorNamesTheValue: the sentinel says what is wrong in
// general; the wrapped message says which value and which limit, because
// "invalid timestamp" alone sends a caller reading their own code instead of
// their data.
func TestInvalidTimestampErrorNamesTheValue(t *testing.T) {
	tests := []struct {
		name string
		ts   time.Time
		want string
	}{
		{"year", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "year 10000"},
		{"offset", time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("far", 25*3600)), "zone offset 90000s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := at(tt.ts).Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want a message containing %q", err, tt.want)
			}
		})
	}
}
