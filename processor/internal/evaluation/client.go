// Package evaluation posts Trustvian DecisionRecords to a control plane's
// versioned /v1 HTTP API.
//
// It is an adapter and nothing more: it decides no evaluation rule, computes
// no diff, scorecard or gate, and holds no platform type. The control plane
// owns all of that (ADR 0031), and this module must not import
// trustvian-platform to reach it (ADR 0022, ADR 0033) — so the contract is
// spoken as JSON over HTTP with the client-side DTOs below, the same
// arrangement cmd/trustvian uses for the CLI.
//
// No retries, no queue, no background goroutine. Ingest carries explicit
// sequence semantics, and a retry layer that did not understand them would
// turn a network blip into duplicated evidence.
package evaluation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	trustvian "github.com/trustvian/trustvian"
)

const (
	// wireVersion is the ingest envelope version this build speaks. It
	// versions the DecisionRecord wrapper, not the /v1 path — task 050 put
	// record versioning in the transport deliberately.
	wireVersion = "1"

	// requestTimeout bounds one request. A Collector that hung on an
	// unreachable control plane would stall its whole trace pipeline.
	requestTimeout = 30 * time.Second

	// maxRequestBody mirrors the server's own cap, so this client never
	// knowingly builds a request the server must reject. Checked on the
	// encoded body, because the envelope's own fields count toward it.
	maxRequestBody = 256 << 10

	// maxResponseBody bounds what is read back. Both responses are small
	// fixed shapes — a disposition and three counters — so this is far
	// tighter than the CLI's 4 MiB comparison bound. A client reading an
	// unbounded body trusts the server's good behaviour for its own memory
	// safety.
	maxResponseBody = 64 << 10
)

// Dispositions the control plane may report. Two values, not three: a
// sequence conflict is an error, and presenting it as a success state would
// let a caller treat "your record was not applied" as progress.
const (
	dispositionApplied  = "applied"
	dispositionReplayed = "replayed"
)

// runStatusRunning is the one run status ingest may proceed against.
// IngestDecisionRecord (platform/controlplane.go) refuses every record for a
// run that is not running, so a Collector that starts up against a run that
// was never started — or one that already finished — would otherwise pass
// Initialize and then fail every span from the first one onward. Checking the
// status here, once, turns that into a clear startup refusal instead.
const runStatusRunning = "running"

// client issues one-shot requests against a control plane.
type client struct {
	baseURL *url.URL
	http    *http.Client
}

// newClient validates the URL before any request is attempted.
func newClient(raw string, timeout time.Duration) (*client, error) {
	base, err := ParseAPIURL(raw)
	if err != nil {
		return nil, err
	}
	return &client{
		baseURL: base,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				// A mutation addressed to one host must not silently become a
				// mutation against another. Redacted() keeps any credential
				// the redirect target carries out of the diagnostic.
				return fmt.Errorf("refusing to follow redirect to %s", req.URL.Redacted())
			},
		},
	}, nil
}

// ParseAPIURL enforces the shape api_url is allowed to take.
//
// Exported so the processor's configuration validation applies the same rules
// the client will, rather than a second copy that can drift from it.
//
// Rejecting confusing forms rather than normalizing them: a URL the operator
// did not mean is better refused than quietly rewritten into one that works.
func ParseAPIURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("api_url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// The parse error can quote the input, which may hold a password.
		return nil, errors.New("api_url is not a valid URL")
	}

	// Checked first and reported without echoing the value: a URL is the
	// wrong place for a secret, and repeating it would put it in Collector
	// logs — the exact exposure this rejection exists to prevent.
	if parsed.User != nil {
		return nil, errors.New("api_url must not contain embedded credentials")
	}

	switch parsed.Scheme {
	case "http", "https":
	case "":
		return nil, errors.New("api_url must be absolute, with an http or https scheme")
	default:
		return nil, fmt.Errorf("api_url scheme %q is not supported; use http or https", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, errors.New("api_url must include a host")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return nil, errors.New("api_url must not contain a query string")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("api_url must not contain a fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		// A base path would have to be composed with every route, and the
		// composition rules are exactly where a client starts addressing
		// something it did not mean.
		return nil, fmt.Errorf("api_url must not contain a path; got %q", parsed.Path)
	}
	return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}, nil
}

// formatSequence renders a sequence for the wire.
//
// Canonical decimal text rather than a JSON number: uint64 exceeds what a
// JSON double represents exactly. This mirrors platform.FormatSequence,
// which this module cannot import.
func formatSequence(v uint64) string { return strconv.FormatUint(v, 10) }

// parseSequence decodes a wire sequence, rejecting anything non-canonical.
//
// Mirrors platform.ParseSequence. A value needing coercion was not produced
// by a server following this contract, and guessing its intent is how a
// cursor silently desynchronizes.
func parseSequence(s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sequence %q is not a canonical uint64", s)
	}
	if strconv.FormatUint(v, 10) != s {
		return 0, fmt.Errorf("sequence %q is not canonical", s)
	}
	if v == 0 {
		return 0, errors.New("sequences start at 1")
	}
	return v, nil
}

// ---------------------------------------------------------------------
// Wire DTOs. These own the JSON; nothing here is a platform type.
// ---------------------------------------------------------------------

type ingestEnvelope struct {
	Version           string                   `json:"version"`
	Sequence          string                   `json:"sequence"`
	BehavioralProfile string                   `json:"behavioral_profile"`
	Record            trustvian.DecisionRecord `json:"record"`
}

// ingestStateBody and ingestBody are decoded leniently — never with
// DisallowUnknownFields. The compatibility contract requires clients to
// tolerate additive response fields, and rejecting them would make every
// additive server change breaking.
type ingestStateBody struct {
	Version      string `json:"version"`
	RunID        string `json:"run_id"`
	NextSequence string `json:"next_sequence"`
}

type ingestBody struct {
	Version          string `json:"version"`
	Disposition      string `json:"disposition"`
	NextSequence     string `json:"next_sequence"`
	RecordCount      string `json:"record_count"`
	BehaviorComplete bool   `json:"behavior_complete"`
}

// progressBody decodes only the field Initialize needs from
// GET /v1/evaluation-runs/{run_id}/progress. The route's other fields
// (record_count, behavior_observation_count, …) are decimal-string
// counters this client has no use for here; leaving them undecoded is what
// the same lenient-decode discipline as the two bodies above calls for.
type progressBody struct {
	Version string `json:"version"`
	Status  string `json:"status"`
}

type errorEnvelope struct {
	Version string `json:"version"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ingestResult is one accepted record's outcome.
type ingestResult struct {
	Disposition  string
	NextSequence uint64
}

// ---------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------

// ingestState reports the sequence the server expects next for runID.
func (c *client) ingestState(ctx context.Context, runID string) (uint64, error) {
	status, payload, err := c.do(ctx, http.MethodGet, nil, "evaluation-runs", runID, "ingest-state")
	if err != nil {
		return 0, err
	}
	if err := checkStatus(status, payload); err != nil {
		return 0, err
	}
	var body ingestStateBody
	if err := json.Unmarshal(payload, &body); err != nil {
		return 0, errors.New("ingest-state response is not valid JSON for this endpoint")
	}
	next, err := parseSequence(body.NextSequence)
	if err != nil {
		return 0, fmt.Errorf("ingest-state: %w", err)
	}
	return next, nil
}

// runStatus reports a run's current lifecycle status, read from the same
// progress route the end-to-end test already drives — so this adds no new
// platform endpoint, only a new caller of an existing one.
func (c *client) runStatus(ctx context.Context, runID string) (string, error) {
	status, payload, err := c.do(ctx, http.MethodGet, nil, "evaluation-runs", runID, "progress")
	if err != nil {
		return "", err
	}
	if err := checkStatus(status, payload); err != nil {
		return "", err
	}
	var body progressBody
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", errors.New("progress response is not valid JSON for this endpoint")
	}
	if body.Status == "" {
		return "", errors.New("progress response carries no status")
	}
	return body.Status, nil
}

// ingest posts one record under an explicit sequence.
func (c *client) ingest(
	ctx context.Context, runID string, sequence uint64,
	profile string, record trustvian.DecisionRecord,
) (ingestResult, error) {
	envelope := ingestEnvelope{
		Version:           wireVersion,
		Sequence:          formatSequence(sequence),
		BehavioralProfile: profile,
		Record:            record,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return ingestResult{}, fmt.Errorf("encoding ingest envelope: %w", err)
	}
	if len(encoded) > maxRequestBody {
		return ingestResult{}, fmt.Errorf(
			"ingest body is %d bytes, over the %d byte limit", len(encoded), maxRequestBody)
	}

	status, payload, err := c.do(ctx, http.MethodPost, encoded, "evaluation-runs", runID, "records")
	if err != nil {
		return ingestResult{}, err
	}
	if err := checkStatus(status, payload); err != nil {
		return ingestResult{}, err
	}

	var body ingestBody
	if err := json.Unmarshal(payload, &body); err != nil {
		return ingestResult{}, errors.New("ingest response is not valid JSON for this endpoint")
	}
	next, err := parseSequence(body.NextSequence)
	if err != nil {
		return ingestResult{}, fmt.Errorf("ingest response: %w", err)
	}
	return ingestResult{Disposition: body.Disposition, NextSequence: next}, nil
}

// apiPath builds a /v1 path with every caller-owned segment escaped.
//
// Both Path and RawPath are set so net/url emits the escaped form: setting
// Path alone would leave "/" intact inside a run ID, letting a caller change
// the route's shape.
func apiPath(segments ...string) (string, string) {
	var decoded, escaped strings.Builder
	decoded.WriteString("/v1")
	escaped.WriteString("/v1")
	for _, segment := range segments {
		decoded.WriteString("/")
		decoded.WriteString(segment)
		escaped.WriteString("/")
		escaped.WriteString(url.PathEscape(segment))
	}
	return decoded.String(), escaped.String()
}

func (c *client) do(
	ctx context.Context, method string, body []byte, segments ...string,
) (int, []byte, error) {
	target := *c.baseURL
	target.Path, target.RawPath = apiPath(segments...)

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return 0, nil, fmt.Errorf("building request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	// No Authorization header: task 070 owns authentication, and a header
	// invented here would freeze a credential shape before there is anything
	// to authenticate against.

	response, err := c.http.Do(request)
	if err != nil {
		// Timeouts, connection failures and the refused redirect all arrive
		// here and all mean the same thing: the operation did not happen.
		// Redacted() because the wrapped *url.Error carries the URL.
		return 0, nil, fmt.Errorf("%s %s: %w", method, target.Redacted(), unwrapURLError(err))
	}
	defer response.Body.Close()

	// limit+1 so "exactly at the limit" stays acceptable and one byte over is
	// detectable without reading the rest of whatever is being sent.
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		return 0, nil, fmt.Errorf("reading response: %w", err)
	}
	if len(payload) > maxResponseBody {
		return 0, nil, fmt.Errorf("response exceeds the %d byte limit", maxResponseBody)
	}
	return response.StatusCode, payload, nil
}

// unwrapURLError strips the *url.Error wrapper's repeated method and URL,
// which the caller already prints.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// checkStatus turns a non-2xx response into an error carrying the server's
// own code and message when it sent a well-formed envelope.
//
// The raw body is never echoed when the envelope is unusable: it may be
// arbitrarily large or not text at all, and the status is the part a caller
// can act on.
func checkStatus(status int, payload []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(payload, &envelope); err == nil && envelope.Error.Code != "" {
		return fmt.Errorf("control plane refused the request: %s: %s",
			envelope.Error.Code, envelope.Error.Message)
	}
	return fmt.Errorf("control plane returned HTTP %d", status)
}
