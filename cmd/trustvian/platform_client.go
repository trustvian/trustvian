package main

// The control-plane HTTP client.
//
// This is the CLI's only edge onto the platform. It speaks the versioned /v1
// API and imports nothing from the trustvian-platform module — see
// docs/adr/0033-developer-cli-is-a-thin-http-adapter.md for why that edge must
// not exist, and cli_architecture_test.go for the check that keeps it absent.
//
// A transport helper, deliberately not a framework: no cache, no persistent
// state, no retries. Retry safety is per-operation — a GET is safe, a
// lifecycle POST is not, and ingest already carries explicit sequence and
// replay semantics — so a generic retry layer would need knowledge it does not
// have.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Exit codes for the platform command families.
//
// Deliberately skipping 1, which legacy commands already use for "the command
// failed". Leaving it unclaimed here is what lets `eval compare` give it a
// single unambiguous meaning; see docs/compatibility.md.
const (
	exitOK          = 0
	exitGateFail    = 1 // eval compare only
	exitUsage       = 2
	exitOperational = 3
)

const (
	// platformRequestTimeout bounds one request.
	//
	// These are one-shot operations; a CLI that hangs forever on an
	// unreachable control plane is a stuck CI job. Not for SSE — task 060
	// consumes no stream, and a long-lived connection needs a different model
	// entirely.
	platformRequestTimeout = 30 * time.Second

	// maxPlatformRequestBody mirrors task 058's server-side cap, so the CLI
	// does not knowingly build a request the server must reject. Checked
	// against the encoded body, because envelope overhead counts.
	maxPlatformRequestBody = 256 << 10

	// maxPlatformResponseBody bounds what the CLI will read back.
	//
	// Sized for the largest real response: a comparison carrying the bounded
	// union of up to 1024 behavioral deltas plus bounded text fields. A
	// client reading an unbounded body trusts the server's good behaviour for
	// its own memory safety, which is not a property worth assuming even
	// locally.
	maxPlatformResponseBody = 4 << 20
)

// platformError carries the documented exit code for a failed command.
//
// The code travels with the error rather than being decided at the call site,
// so a new operation cannot accidentally report a network failure as a usage
// error — or, worse, as a gate result.
type platformError struct {
	code int
	err  error

	// envelope is the server's raw error body, when it sent a well-formed
	// one. Forwarded verbatim in --json mode rather than re-encoded, so the
	// CLI never invents a second error schema.
	envelope []byte
}

func (e *platformError) Error() string { return e.err.Error() }
func (e *platformError) Unwrap() error { return e.err }

func usageErrorf(format string, args ...any) *platformError {
	return &platformError{code: exitUsage, err: fmt.Errorf(format, args...)}
}

func operationalErrorf(format string, args ...any) *platformError {
	return &platformError{code: exitOperational, err: fmt.Errorf(format, args...)}
}

// exitCodeFor maps an error to its documented exit code.
//
// An error that is not a platformError is a bug rather than a condition, and
// is reported as operational: silently treating it as a usage error would tell
// the caller to fix their invocation for something they did not cause.
func exitCodeFor(err error) int {
	if err == nil {
		return exitOK
	}
	var perr *platformError
	if errors.As(err, &perr) {
		return perr.code
	}
	return exitOperational
}

// platformClient issues one-shot requests against a control plane.
type platformClient struct {
	baseURL *url.URL
	client  *http.Client
}

// newPlatformClient validates the URL before any request is attempted.
//
// Rejecting confusing forms rather than normalizing them: a URL the caller did
// not mean is better refused than quietly rewritten into one that works.
func newPlatformClient(raw string, timeout time.Duration) (*platformClient, error) {
	base, err := parseAPIURL(raw)
	if err != nil {
		return nil, err
	}
	return &platformClient{
		baseURL: base,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// A mutation addressed to one host must not silently become a
				// mutation against another. Redacted() keeps any credentials
				// the redirect target carries out of the diagnostic.
				return fmt.Errorf("refusing to follow redirect to %s", req.URL.Redacted())
			},
		},
	}, nil
}

// parseAPIURL enforces the shape --api-url is allowed to take.
func parseAPIURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, usageErrorf("--api-url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// The parse error can quote the input, which may hold a password.
		return nil, usageErrorf("--api-url is not a valid URL")
	}

	// Checked first and reported without echoing the value: a URL is the
	// wrong place for a secret, and repeating it would put it in shell
	// history, CI logs and process listings — the exact exposure the
	// rejection exists to prevent.
	if parsed.User != nil {
		return nil, usageErrorf("--api-url must not contain embedded credentials")
	}

	switch parsed.Scheme {
	case "http", "https":
	case "":
		return nil, usageErrorf("--api-url must be absolute, with an http or https scheme")
	default:
		return nil, usageErrorf("--api-url scheme %q is not supported; use http or https", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, usageErrorf("--api-url must include a host")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return nil, usageErrorf("--api-url must not contain a query string")
	}
	if parsed.Fragment != "" {
		return nil, usageErrorf("--api-url must not contain a fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		// A base path would have to be composed with every route, and the
		// composition rules (trailing slash, escaping, double slashes) are
		// exactly where a client starts addressing something it did not mean.
		return nil, usageErrorf("--api-url must not contain a path; got %q", parsed.Path)
	}
	return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}, nil
}

// apiPath builds a /v1 path with every caller-owned segment escaped.
//
// Both Path and RawPath are set so net/url emits the escaped form: setting
// Path alone would leave "/" intact inside an identifier, letting a caller
// change the route's shape. "a/b" must address one resource named "a/b", never
// two segments.
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

// apiResult is one completed exchange.
type apiResult struct {
	status int
	body   []byte
}

// get issues a GET and returns the decoded result.
func (c *platformClient) get(ctx context.Context, segments ...string) (apiResult, error) {
	return c.do(ctx, http.MethodGet, nil, segments...)
}

// post issues a POST carrying a JSON body, or no body when payload is nil.
func (c *platformClient) post(ctx context.Context, payload any, segments ...string) (apiResult, error) {
	var encoded []byte
	if payload != nil {
		var err error
		encoded, err = json.Marshal(payload)
		if err != nil {
			return apiResult{}, operationalErrorf("encoding request: %v", err)
		}
		// Checked on the encoded body rather than on any input file, because
		// the envelope's own fields count toward the server's limit.
		if len(encoded) > maxPlatformRequestBody {
			return apiResult{}, usageErrorf(
				"request body is %d bytes, over the %d byte limit", len(encoded), maxPlatformRequestBody)
		}
	}
	return c.do(ctx, http.MethodPost, encoded, segments...)
}

func (c *platformClient) do(
	ctx context.Context, method string, body []byte, segments ...string,
) (apiResult, error) {
	target := *c.baseURL
	target.Path, target.RawPath = apiPath(segments...)

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return apiResult{}, operationalErrorf("building request: %v", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	// No Authorization header: task 070 owns authentication, and a header
	// invented here would freeze a credential shape before there is anything
	// to authenticate against.

	response, err := c.client.Do(request)
	if err != nil {
		// Timeouts, connection failures and the refused redirect all arrive
		// here, and all mean the same thing to a caller: the operation did not
		// happen. Redacted() because the wrapped *url.Error carries the URL.
		return apiResult{}, operationalErrorf("%s %s: %v", method, target.Redacted(), unwrapURLError(err))
	}
	defer response.Body.Close()

	// limit+1 so "exactly at the limit" stays acceptable and one byte over is
	// detectable without reading the rest of whatever is being sent.
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxPlatformResponseBody+1))
	if err != nil {
		return apiResult{}, operationalErrorf("reading response: %v", err)
	}
	if len(payload) > maxPlatformResponseBody {
		return apiResult{}, operationalErrorf(
			"response exceeds the %d byte limit", maxPlatformResponseBody)
	}
	return apiResult{status: response.StatusCode, body: payload}, nil
}

// unwrapURLError strips the *url.Error wrapper's repeated method and URL.
//
// The caller already prints both; repeating them makes the useful part of the
// message harder to find.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// apiErrorEnvelope is the control plane's error shape.
//
// Decoded leniently, like every response: an additive field must not turn an
// error the CLI could have reported into one it cannot.
type apiErrorEnvelope struct {
	Version string `json:"version"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// checkStatus turns a non-2xx response into an operational error.
//
// Every API failure is exit 3, whatever the status: a 409 is not a gate
// failure, a 404 is not a gate failure, and conflating them would let a CI
// script report a missing run as a policy violation.
func checkStatus(result apiResult) error {
	if result.status >= 200 && result.status < 300 {
		return nil
	}

	var envelope apiErrorEnvelope
	if err := json.Unmarshal(result.body, &envelope); err == nil && envelope.Error.Code != "" {
		return &platformError{
			code:     exitOperational,
			err:      fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message),
			envelope: result.body,
		}
	}
	// No usable envelope. The body is not echoed: it may be arbitrarily large
	// or not text at all, and the status is the part a caller can act on.
	return operationalErrorf("server returned HTTP %d", result.status)
}

// decodeJSON reads a response into a rendering DTO.
//
// Never DisallowUnknownFields. The compatibility contract requires clients to
// tolerate additive response fields, and rejecting them would make every
// additive server change breaking — inverting the contract this client exists
// to consume.
func decodeJSON(body []byte, target any) error {
	if err := json.Unmarshal(body, target); err != nil {
		return operationalErrorf("server response is not valid JSON for this endpoint")
	}
	return nil
}
