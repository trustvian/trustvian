package status

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// maxReportBody mirrors the control plane's cap on a status report. Checked
	// on the encoded body, so a report this package builds is never refused for
	// its size; if one somehow were, it is dropped and the next interval tries
	// again rather than truncating evidence into a misleading shape.
	maxReportBody = 64 << 10

	// maxResponseBody bounds what is read back from an acknowledgement.
	maxResponseBody = 4 << 10

	// maxRequestTimeout caps one POST, so a slow control plane can delay a
	// report but never stack them: the loop below is synchronous, and a
	// request outliving its interval would be the only way two could overlap.
	maxRequestTimeout = 5 * time.Second
)

// SnapshotFunc builds the body of the next report, minus its identity and
// sequence, which the Reporter owns.
type SnapshotFunc func(now time.Time) Report

// Reporter pushes a status report to the control plane on a fixed interval.
//
// One goroutine, one request at a time, no queue. Each tick builds a fresh
// snapshot of cumulative counters, so a report that failed to send is simply
// superseded by the next — there is nothing to retry, buffer or replay, and a
// slow or absent control plane cannot grow this process's memory.
type Reporter struct {
	endpoint    string
	collectorID string
	instance    string
	interval    time.Duration
	snapshot    SnapshotFunc
	client      *http.Client
	now         func() time.Time

	// warn reports a failed send. Called on the first failure after a success
	// (and on the first ever), never on each repeat, so an absent control
	// plane produces one line rather than one per interval.
	warn func(error)

	sequence uint64
	failing  bool

	started  atomic.Bool
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// NewReporter validates its inputs and returns a stopped Reporter.
//
// apiBase must already have been checked by the caller's URL parser; this
// only composes the route. collectorID names this Collector stably across
// restarts when the operator chose one; instance distinguishes one process
// from the next, so a restarted Collector's reports are never mistaken for an
// older process's out-of-order ones.
func NewReporter(
	apiBase *url.URL, collectorID string, interval time.Duration,
	snapshot SnapshotFunc, warn func(error),
) (*Reporter, error) {
	if apiBase == nil {
		return nil, errors.New("status: an API URL is required")
	}
	if collectorID == "" {
		return nil, errors.New("status: a collector ID is required")
	}
	if interval <= 0 {
		return nil, errors.New("status: the interval must be positive")
	}
	if snapshot == nil {
		return nil, errors.New("status: a snapshot function is required")
	}
	instance, err := NewInstance()
	if err != nil {
		return nil, err
	}
	target := *apiBase
	target.Path = "/v1/collectors/" + collectorID + "/status"
	target.RawPath = "/v1/collectors/" + url.PathEscape(collectorID) + "/status"

	timeout := min(interval, maxRequestTimeout)
	if warn == nil {
		warn = func(error) {}
	}
	return &Reporter{
		endpoint:    target.String(),
		collectorID: collectorID,
		instance:    instance,
		interval:    interval,
		snapshot:    snapshot,
		now:         time.Now,
		warn:        warn,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Redacted())
			},
		},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}, nil
}

// NewInstance returns a random identifier for one process.
func NewInstance() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("status: generating an instance identifier: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Instance is this process's identifier.
func (r *Reporter) Instance() string { return r.instance }

// Start sends one report immediately and then one per interval, until Stop.
// A second call is ignored.
func (r *Reporter) Start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	go r.loop()
}

func (r *Reporter) loop() {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.send()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.send()
		}
	}
}

// Stop ends the loop and waits for it, bounded by ctx. Idempotent, and safe
// to call on a Reporter that was never started.
func (r *Reporter) Stop(ctx context.Context) error {
	r.stopOnce.Do(func() { close(r.stop) })
	if !r.started.Load() {
		return nil
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// send builds and posts one report. Failures are reported through warn and
// otherwise ignored: the next tick carries newer counters anyway.
func (r *Reporter) send() {
	r.sequence++
	report := r.snapshot(r.now())
	report.Version = WireVersion
	report.CollectorID = r.collectorID
	report.Instance = r.instance
	report.Sequence = formatUint(r.sequence)

	err := r.post(report)
	switch {
	case err != nil && !r.failing:
		r.failing = true
		r.warn(err)
	case err == nil:
		r.failing = false
	}
}

func (r *Reporter) post(report Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encoding the status report: %w", err)
	}
	if len(body) > maxReportBody {
		return fmt.Errorf("the status report is %d bytes, over the %d byte limit", len(body), maxReportBody)
	}

	// Bounded by the client's own timeout, and abandoned at Stop: a shutdown
	// must not wait on a control plane that is not answering.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-r.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building the status request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := r.client.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("posting the status report: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("the control plane refused the status report with HTTP %d", response.StatusCode)
	}
	return nil
}

func formatUint(v uint64) string { return strconv.FormatUint(v, 10) }
