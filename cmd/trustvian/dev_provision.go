package main

// Provisioning the platform hierarchy `trustvian dev` needs, over `/v1`.
//
// Every object is created through the versioned HTTP API, never in process. dev
// is a client (ADR 0033), the control plane owns entity lifecycle (ADR 0031), and
// the root module cannot import the platform anyway (ADR 0022, 0035). What this
// file contains is the order, the idempotence, and the run's lifecycle.
//
// The order is the demo prototype's, arrived at by running into each failure
// rather than by reading:
//
//	project -> environment -> agent -> candidate -> run created -> run started
//	  -> Collector -> workload
//
// Two steps in it are not obvious and cost a debugging session each. The
// environment is created before any run names one, because a run may only name an
// environment its project already owns. And the run is *started* before the
// Collector launches, because ingest is refused for a pending run and the
// processor's sink reads its cursor at startup.

import (
	"context"
	"fmt"
	"net/http"
)

// provisionTimeout bounds each individual /v1 call.
//
// The same order as the other platform families' default: these are local
// loopback calls that answer in milliseconds, and the bound only matters when
// something is wrong.
const provisionTimeout = platformRequestTimeout

// provisioner creates and drives what one dev invocation needs.
type provisioner struct {
	client   *platformClient
	identity devIdentity
}

// newProvisioner builds a client for the control plane dev is using.
//
// Through resolveAPIURL with the URL marked explicit, not through the client
// constructor directly. dev already knows its endpoint authoritatively — either
// the caller gave it or dev started the runtime that published it — so from the
// resolver's point of view that is an explicitly supplied value, which is
// precisely the case it handles by using it as given and reading no discovery
// file.
//
// Routing through it anyway keeps one path to a client for every command, which
// is the property TestDiscoveryResolverIsTheSinglePathToAClient exists to
// protect. A second construction site is how one command silently loses
// discovery.
func newProvisioner(apiURL string, identity devIdentity) (*provisioner, error) {
	client, err := resolveAPIURL(apiURL, true, provisionTimeout)
	if err != nil {
		return nil, err
	}
	return &provisioner{client: client, identity: identity}, nil
}

// ensureHierarchy creates every object that does not exist yet.
//
// Idempotent, and that is a requirement rather than a nicety: many dev runs share
// one control plane, and creating unconditionally would answer 409 already_exists
// on the second run and take it down. Existence is asked of the server rather
// than inferred from a fresh database, because the database may not be fresh.
func (p *provisioner) ensureHierarchy(ctx context.Context) error {
	if err := p.ensure(ctx,
		"project", []string{"projects", p.identity.Project},
		map[string]any{"id": p.identity.Project, "name": p.identity.ProjectName},
		[]string{"projects"}); err != nil {
		return err
	}

	// Before any run names it. A new environment is active on creation, which is
	// the state a run requires; a run naming one its project does not own is
	// refused.
	if err := p.ensure(ctx,
		"environment", []string{"projects", p.identity.Project, "environments", p.identity.Environment},
		map[string]any{
			"project_id": p.identity.Project,
			"ref":        p.identity.Environment,
			"name":       p.identity.Environment,
		},
		[]string{"environments"}); err != nil {
		return err
	}

	if err := p.ensure(ctx,
		"agent", []string{"agents", p.identity.Agent},
		map[string]any{
			"id":         p.identity.Agent,
			"project_id": p.identity.Project,
			"name":       p.identity.AgentName,
		},
		[]string{"agents"}); err != nil {
		return err
	}

	return p.ensure(ctx,
		"candidate", []string{"candidates", p.identity.Candidate},
		map[string]any{
			"id":       p.identity.Candidate,
			"agent_id": p.identity.Agent,
		},
		[]string{"candidates"})
}

// ensure creates one object unless it already exists.
//
// GET first, then POST. The reverse — POST and tolerate 409 — reads as fewer
// calls and is worse: it makes "already exists" indistinguishable from a
// conflicting definition, and on a shared control plane the second run of a
// stability sweep would have to treat a real conflict as success.
func (p *provisioner) ensure(ctx context.Context, kind string,
	getSegments []string, body map[string]any, postSegments []string) error {
	result, err := p.client.get(ctx, getSegments...)
	if err != nil {
		// A transport failure, not an answer. Reported as itself rather than
		// treated as absence, because creating in response to an unreachable
		// server is how a second object gets made.
		return fmt.Errorf("checking whether the %s exists: %w", kind, err)
	}
	if result.status >= 200 && result.status < 300 {
		return nil
	}
	if result.status != http.StatusNotFound {
		// Anything other than "it is not there" is a real failure and must not
		// be papered over by an attempt to create.
		return fmt.Errorf("checking whether the %s exists: %w", kind, checkStatus(result))
	}

	created, err := p.client.post(ctx, body, postSegments...)
	if err != nil {
		return fmt.Errorf("creating the %s: %w", kind, err)
	}
	if err := checkStatus(created); err != nil {
		return fmt.Errorf("creating the %s: %w", kind, err)
	}
	return nil
}

// startRun creates the evaluation run and moves it to running.
//
// Two calls rather than one, because the platform models creation and start
// separately (ADR 0031) and dev does not get to collapse them.
func (p *provisioner) startRun(ctx context.Context) error {
	created, err := p.client.post(ctx, map[string]any{
		"id":                 p.identity.Run,
		"candidate_id":       p.identity.Candidate,
		"environment":        p.identity.Environment,
		"behavioral_profile": p.identity.Profile,
	}, "evaluation-runs")
	if err != nil {
		return fmt.Errorf("creating the evaluation run: %w", err)
	}
	if err := checkStatus(created); err != nil {
		return fmt.Errorf("creating the evaluation run: %w", err)
	}

	started, err := p.client.post(ctx, nil, "evaluation-runs", p.identity.Run, "start")
	if err != nil {
		return fmt.Errorf("starting the evaluation run: %w", err)
	}
	if err := checkStatus(started); err != nil {
		return fmt.Errorf("starting the evaluation run: %w", err)
	}
	return nil
}

// completeRun ends a run whose workload succeeded.
func (p *provisioner) completeRun(ctx context.Context) error {
	result, err := p.client.post(ctx, nil, "evaluation-runs", p.identity.Run, "complete")
	if err != nil {
		return fmt.Errorf("completing the evaluation run: %w", err)
	}
	if err := checkStatus(result); err != nil {
		return fmt.Errorf("completing the evaluation run: %w", err)
	}
	return nil
}

// failRun ends a run whose workload did not succeed.
//
// **Failed, not completed**, and this is the distinction that matters most in the
// whole file. A workload that exited non-zero did not produce a behavioral
// regression — it produced no verdict at all. Completing the run would leave
// evidence that a later `eval compare` would read as a finished evaluation of a
// candidate, when what actually happened is that the program crashed.
//
// The reason is bounded and carries only the exit status: a workload's own output
// is not summarized into platform state.
func (p *provisioner) failRun(ctx context.Context, exitCode int) error {
	reason := fmt.Sprintf("the workload exited %d under trustvian dev", exitCode)
	result, err := p.client.post(ctx, map[string]any{"reason": reason},
		"evaluation-runs", p.identity.Run, "fail")
	if err != nil {
		return fmt.Errorf("failing the evaluation run: %w", err)
	}
	if err := checkStatus(result); err != nil {
		return fmt.Errorf("failing the evaluation run: %w", err)
	}
	return nil
}

// lifecycleContext bounds the calls that happen after the workload ends.
//
// A fresh context, deliberately: the one the workload ran under may already be
// cancelled by the signal that ended it, and a cancelled context would skip the
// call that records what happened — which is the one call that must not be
// skipped. Ctrl-C must still leave the run in a terminal state.
func lifecycleContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), provisionTimeout)
}
