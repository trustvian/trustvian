package main

// Posting one rendering as the pull request's one gate comment for a marker.
//
// The comment is found, never assumed: the poster lists the pull request's
// issue comments and takes as its own only one that github-actions[bot]
// wrote and whose body begins with the exact marker line. A human comment
// carrying the marker, or the marker quoted anywhere but the first line, is
// someone else's and is never edited.
//
// Just before writing, it asks GitHub for the pull request's head. If a newer
// commit has been pushed, a newer run owns the comment, and this one writes
// nothing rather than replacing a newer verdict with an older one.
//
// Creating is the one write that is not idempotent: a POST that failed may
// still have landed. So a failed POST is not simply sent again — the comments
// are listed afresh, and a comment that landed is edited rather than
// duplicated.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// botLogin is the login GitHub gives GITHUB_TOKEN's writes. A user login
// cannot contain brackets, so no person can hold it.
const botLogin = "github-actions[bot]"

const (
	// perPage is the most comments GitHub returns per page.
	perPage = 100
	// maxPages bounds the search: 3,000 comments.
	maxPages = 30
)

// marker is the first line of every comment the poster owns for id.
func marker(id string) string { return "<!-- trustvian-behavioral-gate:" + id + " -->" }

// owns reports whether c is a comment this poster wrote for id.
func owns(c issueComment, id string) bool {
	if c.User.Login != botLogin {
		return false
	}
	first, _, _ := strings.Cut(c.Body, "\n")
	return strings.TrimSuffix(first, "\r") == marker(id)
}

// outcome is what a post did.
type outcome int

const (
	created outcome = iota
	updated
	superseded
	notPermitted
)

type postRequest struct {
	owner, repo string
	number      int
	headSHA     string
	id          string
	body        string // the rendering, without the marker
}

type postResult struct {
	outcome    outcome
	commentID  int64
	newerHead  string // when superseded
	duplicates int    // owned comments beyond the one updated
	truncated  bool   // the search stopped at maxPages
}

var shaText = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type commentBody struct {
	Body string `json:"body"`
}

// post brings the pull request's comment for r.id into line with r.body.
func post(ctx context.Context, c client, r postRequest) (postResult, error) {
	p := poster{c: c, r: r, repo: "/repos/" + r.owner + "/" + r.repo,
		body: commentBody{marker(r.id) + "\n" + r.body}}
	ours, err := p.list(ctx)
	if err != nil {
		return p.res, err
	}
	if newer, err := p.newerHead(ctx); err != nil || newer != "" {
		p.res.outcome, p.res.newerHead = superseded, newer
		return p.res, err
	}
	if len(ours) > 0 {
		return p.update(ctx, ours)
	}

	// Create. Not retried blindly: a POST that failed may have landed.
	var made issueComment
	wait, err := c.attempt(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", p.repo, r.number), p.body, &made)
	if errors.Is(err, errRetryable) {
		c.sleep(wait)
		if ours, err = p.list(ctx); err != nil {
			return p.res, err
		}
		if len(ours) > 0 {
			return p.update(ctx, ours)
		}
		_, err = c.attempt(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", p.repo, r.number), p.body, &made)
		if err != nil {
			err = fmt.Errorf("%w (after one retry)", err)
		}
	}
	if err != nil {
		return p.writeFailed(err)
	}
	p.res.outcome, p.res.commentID = created, made.ID
	return p.res, nil
}

type poster struct {
	c    client
	r    postRequest
	repo string
	body commentBody
	res  postResult
}

// list returns every comment the poster owns for its id, searching at most
// maxPages pages.
func (p *poster) list(ctx context.Context) ([]issueComment, error) {
	var ours []issueComment
	p.res.truncated = false
	for page := 1; page <= maxPages; page++ {
		var batch []issueComment
		path := fmt.Sprintf("%s/issues/%d/comments?per_page=%d&page=%d", p.repo, p.r.number, perPage, page)
		if err := p.c.do(ctx, "GET", path, nil, &batch); err != nil {
			return nil, readFailed(err)
		}
		for _, comment := range batch {
			if owns(comment, p.r.id) {
				ours = append(ours, comment)
			}
		}
		if len(batch) < perPage {
			break
		}
		p.res.truncated = page == maxPages
	}
	return ours, nil
}

// newerHead reads the pull request's head and returns it when it is not the
// head this rendering describes. A head GitHub does not name as a commit is
// an API failure, never a reason to write nothing.
func (p *poster) newerHead(ctx context.Context) (string, error) {
	var pr pullRequest
	if err := p.c.do(ctx, "GET", fmt.Sprintf("%s/pulls/%d", p.repo, p.r.number), nil, &pr); err != nil {
		return "", readFailed(err)
	}
	if !shaText.MatchString(pr.Head.SHA) {
		return "", fmt.Errorf("%w: the pull request's head is not a commit SHA", errAPI)
	}
	if pr.Head.SHA == p.r.headSHA {
		return "", nil
	}
	return pr.Head.SHA, nil
}

// update edits the newest owned comment. If it was deleted since it was
// listed, the rendering is posted as a new one.
func (p *poster) update(ctx context.Context, ours []issueComment) (postResult, error) {
	newest := ours[0]
	for _, comment := range ours[1:] {
		if comment.ID > newest.ID {
			newest = comment
		}
	}
	p.res.duplicates = len(ours) - 1
	err := p.c.do(ctx, "PATCH", fmt.Sprintf("%s/issues/comments/%d", p.repo, newest.ID), p.body, nil)
	if errors.Is(err, errNotFound) {
		var made issueComment
		if _, err := p.c.attempt(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", p.repo, p.r.number),
			p.body, &made); err != nil {
			return p.writeFailed(err)
		}
		p.res.outcome, p.res.commentID = created, made.ID
		return p.res, nil
	}
	if err != nil {
		return p.writeFailed(err)
	}
	p.res.outcome, p.res.commentID = updated, newest.ID
	return p.res, nil
}

// writeFailed turns a refused write — 403, or GitHub's 404 for a write the
// token may not make — into the notPermitted outcome: the fork and read-only
// path. Every other error is passed on.
func (p *poster) writeFailed(err error) (postResult, error) {
	if errors.Is(err, errForbidden) || errors.Is(err, errNotFound) {
		p.res.outcome = notPermitted
		return p.res, nil
	}
	return p.res, err
}

// readFailed: a 403 to a read is a refusal like any other, but a 404 to a
// read means the repository or number is wrong — a fork's read-only token can
// read the pull request — so it fails rather than degrading.
func readFailed(err error) error {
	if errors.Is(err, errForbidden) {
		return err
	}
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("%w: %w", errAPI, err)
	}
	return err
}
