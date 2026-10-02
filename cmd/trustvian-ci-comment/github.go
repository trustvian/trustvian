package main

// The few GitHub REST calls the poster makes, and how it answers their
// failures.
//
// Every request carries the token in a header and nowhere else; no response
// body is ever echoed, so nothing GitHub — or anything pretending to be it —
// returns reaches the job log. Responses are read under a size bound and
// decoded into the fields the poster uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	// maxResponseBytes bounds one response. A page of 100 issue comments,
	// each up to GitHub's 65,536 characters, fits even with every character
	// JSON-escaped to six bytes.
	maxResponseBytes = 64 << 20

	// requestTimeout bounds each request, retry included separately.
	requestTimeout = 30 * time.Second

	// maxRetryWait caps how long a Retry-After may make the poster wait.
	maxRetryWait = 10 * time.Second

	// defaultRetryWait is the wait before the one retry when GitHub names
	// none.
	defaultRetryWait = 2 * time.Second
)

// errForbidden is GitHub's 403 that is not a rate limit: the token may not do
// what was asked. On a fork pull request, or with a read-only token, that is
// the expected answer to a write.
var errForbidden = errors.New("the token is not permitted to do this")

// errNotFound is a 404. GitHub answers a write the token may not make with
// 404 as well as 403; a 404 to a read is a wrong repository or number, since
// even a fork's read-only token can read the pull request. post decides
// which it is.
var errNotFound = errors.New("not found")

// errRetryable marks a failure that one retry may cure: 429, 5xx, a
// rate-limiting 403, or a transport error.
var errRetryable = errors.New("retryable")

// errAPI reports any other failure: a transport error, a status the poster
// does not expect, or a body it cannot read.
var errAPI = errors.New("the GitHub API request failed")

type client struct {
	http  *http.Client
	base  string // https://host[:port], validated
	token string
	// sleep waits before a retry; tests replace it.
	sleep func(time.Duration)
}

// do sends an idempotent request — a GET or a PATCH, which writes the same
// body twice to the same effect — retrying it once on a retryable failure.
// out, when not nil, receives the decoded JSON body of a 2xx answer.
func (c client) do(ctx context.Context, method, path string, in, out any) error {
	wait, err := c.attempt(ctx, method, path, in, out)
	if !errors.Is(err, errRetryable) {
		return err
	}
	c.sleep(wait)
	if _, err := c.attempt(ctx, method, path, in, out); err != nil {
		return fmt.Errorf("%w (after one retry)", err)
	}
	return nil
}

// attempt sends a request once. A retryable failure wraps errRetryable and
// comes with how long to wait before the retry. A POST goes through attempt
// alone: it is not idempotent, and post decides what a retry of it means.
func (c client) attempt(ctx context.Context, method, path string, in, out any) (time.Duration, error) {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return 0, fmt.Errorf("%w: encoding the request: %v", errAPI, err)
		}
	}
	wait, err := c.once(ctx, method, path, payload, out)
	if err != nil && wait >= 0 {
		return wait, fmt.Errorf("%w: %w", errRetryable, err)
	}
	return 0, err
}

// once sends the request a single time. On failure, wait is how long to wait
// before the one retry, or negative when the failure is not retryable.
func (c client) once(ctx context.Context, method, path string, payload []byte, out any) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return -1, fmt.Errorf("%w: %s %s: %v", errAPI, method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "trustvian-ci-comment")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport failed; whether the request landed is unknown, and
		// one retry of an idempotent outcome is safe: a second PATCH writes
		// the same body, and a duplicate POST is found and reported by the
		// next run.
		return defaultRetryWait, fmt.Errorf("%w: %s %s: the request did not complete", errAPI, method, path)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))

	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 ||
		(resp.StatusCode == http.StatusForbidden && rateLimited(resp.Header)):
		return retryWait(resp.Header), fmt.Errorf("%w: %s %s answered %d", errAPI, method, path, resp.StatusCode)
	case resp.StatusCode == http.StatusForbidden:
		return -1, fmt.Errorf("%w: %s %s answered 403", errForbidden, method, path)
	case resp.StatusCode == http.StatusNotFound:
		return -1, fmt.Errorf("%w: %s %s answered 404", errNotFound, method, path)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// A redirect lands here too: the client follows none, so a token is
		// never sent anywhere but the validated API host.
		return -1, fmt.Errorf("%w: %s %s answered %d", errAPI, method, path, resp.StatusCode)
	}
	if readErr != nil || len(raw) > maxResponseBytes {
		return -1, fmt.Errorf("%w: %s %s: the response could not be read within %d bytes",
			errAPI, method, path, maxResponseBytes)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return -1, fmt.Errorf("%w: %s %s: the response is not the JSON expected", errAPI, method, path)
		}
	}
	return 0, nil
}

// rateLimited reports whether a 403 is GitHub's rate limit rather than a
// refusal: it says when to retry, or that no requests remain.
func rateLimited(h http.Header) bool {
	return h.Get("Retry-After") != "" || h.Get("X-RateLimit-Remaining") == "0"
}

// retryWait is the Retry-After GitHub asked for, in whole seconds, capped;
// otherwise the default.
func retryWait(h http.Header) time.Duration {
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxRetryWait)
	}
	return defaultRetryWait
}

// issueComment is the part of an issue comment the poster reads.
type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

type pullRequest struct {
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}
