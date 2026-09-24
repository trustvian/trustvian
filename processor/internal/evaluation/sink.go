package evaluation

import (
	"context"
	"errors"
	"fmt"
	"sync"

	trustvian "github.com/trustvian/trustvian"
)

// Sink posts DecisionRecords to one evaluation run, in order.
//
// The producer owns the sequence (ADR 0031 §8), so this owns a cursor. The
// cursor is seeded from the server by Initialize and thereafter advances
// only to the number the server returned — nothing is derived locally, so a
// client-side increment can never disagree with durable state.
type Sink struct {
	client  *client
	runID   string
	profile string

	// mu covers allocate → POST → advance as one critical section.
	//
	// An atomic counter would be wrong, not merely coarse. The ingest
	// contract is gap-free and strictly monotonic: two goroutines holding 5
	// and 6 race, and if 6 arrives first the server refuses it as a gap —
	// correctly, since it cannot know 5 is in flight. Reordering would have
	// to be reimplemented here, which is the machinery the explicit-sequence
	// contract exists to avoid.
	//
	// The cost is one loopback round trip per analyzed span, serialized. That
	// is the price of an evaluation-configured Collector, which is dedicated
	// to one run; a Collector with no evaluation block takes no lock at all.
	mu          sync.Mutex
	next        uint64
	initialized bool
}

// New validates the configuration and returns an uninitialized Sink.
//
// Construction never performs I/O, so an unreachable control plane fails at
// Start with a clear message rather than inside pipeline graph construction.
func New(apiURL, runID, behavioralProfile string) (*Sink, error) {
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	if behavioralProfile == "" {
		return nil, errors.New("behavioral_profile is required")
	}
	c, err := newClient(apiURL, requestTimeout)
	if err != nil {
		return nil, err
	}
	return &Sink{client: c, runID: runID, profile: behavioralProfile}, nil
}

// RunID is the run this sink feeds.
func (s *Sink) RunID() string { return s.runID }

// Initialize confirms the run is actually running, then seeds the cursor
// from the control plane.
//
// Called from Start rather than lazily on the first span, so a Collector
// whose control plane is unreachable refuses to come up instead of enriching
// spans for an unknown period while recording nothing. It is also what lets
// a restarted Collector resume a running evaluation rather than restarting
// the count and double-aggregating.
//
// The status check exists because IngestDecisionRecord (the control plane's
// own guard) refuses every record for a run that is not RunRunning — so
// without this, a Collector pointed at a run that was created but never
// started, or one that already finished, would pass Initialize, log ready,
// serve /readyz 200, and then fail every span from the first one onward.
// Checking once here, against the run's own progress, turns that into one
// clear startup refusal naming the actual status instead.
func (s *Sink) Initialize(ctx context.Context) error {
	status, err := s.client.runStatus(ctx, s.runID)
	if err != nil {
		return fmt.Errorf("reading run status for run %s: %w", s.runID, err)
	}
	if status != runStatusRunning {
		return fmt.Errorf(
			"run %s is %s, not running; evaluation ingest requires a run that has already been started",
			s.runID, status)
	}

	next, err := s.client.ingestState(ctx, s.runID)
	if err != nil {
		return fmt.Errorf("reading ingest state for run %s: %w", s.runID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = next
	s.initialized = true
	return nil
}

// Record posts one record and reports the server's disposition.
//
// The cursor advances only on success, and only to the server's number. A
// failed post consumes no sequence: if it did, the next successful record
// would leave a gap the run could never recover from.
func (s *Sink) Record(ctx context.Context, record trustvian.DecisionRecord) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return "", errors.New("evaluation sink used before its ingest state was read")
	}

	sequence := s.next
	result, err := s.client.ingest(ctx, s.runID, sequence, s.profile, record)
	if err != nil {
		return "", err
	}

	switch result.Disposition {
	case dispositionApplied, dispositionReplayed:
	default:
		// Fail closed. An unrecognized disposition may or may not mean the
		// record landed, and recording it as a success would report evidence
		// that might not exist.
		return "", fmt.Errorf("control plane reported unrecognized disposition %q", result.Disposition)
	}

	if result.NextSequence <= sequence {
		// A server claiming success without advancing would freeze the
		// cursor, and every later span would conflict against it forever.
		return "", fmt.Errorf(
			"control plane accepted sequence %d but reports %d next; the cursor did not advance",
			sequence, result.NextSequence)
	}
	s.next = result.NextSequence

	return result.Disposition, nil
}

// nextSequence reports the cursor, for tests.
func (s *Sink) nextSequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}
