package webui

// Task 098: the shared selection context.
//
// Driven under node with deferred promises the test resolves in the order it
// chooses, because the properties that matter — a superseded parent's page
// never lands, a dependent clears when its parent changes, preselection never
// guesses — are about which of two responses arrives last.

import (
	"strings"
	"testing"
)

const contextHarness = `
import { createSelectionContext, unambiguousSingle, MAX_OPTION_ROWS } from "./views/context.js";

// deferred API: every call records itself and waits for the test to settle it.
function deferredAPI() {
  const calls = [];
  const call = (name) => (...args) => new Promise((resolve, reject) => {
    calls.push({ name, args, resolve, reject });
  });
  return {
    calls,
    deps: {
      listProjects: call("projects"),
      listProjectAgents: call("agents"),
      listAgentCandidates: call("candidates"),
      listRecentRuns: call("runs"),
      listAllEnvironments: call("environments"),
      getRun: call("getRun"),
      getCandidate: call("getCandidate"),
      getAgent: call("getAgent"),
      getProject: call("getProject"),
    },
    find(name, arg0) {
      return calls.find((c) => c.name === name && (arg0 === undefined || c.args[0] === arg0) && !c.done);
    },
    async settle(name, arg0, value) {
      for (let i = 0; i < 50 && !this.find(name, arg0); i++) await tick();
      const c = this.find(name, arg0);
      if (!c) throw new Error("no pending " + name + " " + arg0);
      c.done = true;
      c.resolve(value);
    },
    async fail(name, arg0, error) {
      for (let i = 0; i < 50 && !this.find(name, arg0); i++) await tick();
      const c = this.find(name, arg0);
      if (!c) throw new Error("no pending " + name + " " + arg0);
      c.done = true;
      c.reject(error);
    },
  };
}
const tick = () => new Promise((r) => setTimeout(r, 0));
const ids = (page) => page.rows.map((r) => r.id);
const sel = (ctx, level) => (ctx.selection[level] ? ctx.selection[level].id : null);
`

// TestContextClearsDependentsWhenAParentChanges is the dependency rule.
func TestContextClearsDependentsWhenAParentChanges(t *testing.T) {
	driver := contextHarness + `
const api = deferredAPI();
const ctx = createSelectionContext(api.deps);
const out = {};

const p = ctx.choose("project", { id: "p1", name: "One" });
await tick();
await api.settle("agents", "p1", { agents: [{ id: "a1", name: "A" }, { id: "a2" }], next_after: "" });
await api.settle("environments", "p1", { environments: [{ ref: "local" }] });
await p;
const choosingAgent = ctx.choose("agent", { id: "a1", name: "A" });
await api.settle("candidates", "a1", { candidates: [{ id: "c1" }, { id: "c2" }] });
await tick();
await choosingAgent;
const choosingCandidate = ctx.choose("candidate", { id: "c1" });
await api.settle("runs", "p1", { evaluation_runs: [{ id: "r1" }, { id: "r2" }] });
await choosingCandidate;
ctx.assume("run", { id: "r1" });
out.before = { agent: sel(ctx, "agent"), candidate: sel(ctx, "candidate"), run: sel(ctx, "run"),
  candidates: ids(ctx.pages.candidates), runs: ids(ctx.pages.runs) };

// Re-choosing the same agent is not a change.
await ctx.choose("agent", { id: "a1", name: "A" });
out.sameAgent = { candidate: sel(ctx, "candidate"), run: sel(ctx, "run") };

// A different agent clears the candidate, the run and their pages.
const switching = ctx.choose("agent", { id: "a2" });
out.afterAgent = { candidate: sel(ctx, "candidate"), run: sel(ctx, "run"),
  candidates: ids(ctx.pages.candidates), runs: ids(ctx.pages.runs),
  candidatesLoading: ctx.pages.candidates.loading };
await api.settle("candidates", "a2", { candidates: [{ id: "c9" }, { id: "c8" }] });
await switching;

// A different project clears everything below it, environments included.
ctx.choose("project", { id: "p2" });
out.afterProject = { agent: sel(ctx, "agent"), candidate: sel(ctx, "candidate"), run: sel(ctx, "run"),
  agents: ids(ctx.pages.agents), environments: ctx.pages.environments.rows.length };
process.stdout.write(JSON.stringify(out));
`
	var got struct {
		Before struct {
			Agent, Candidate, Run string
			Candidates, Runs      []string
		}
		SameAgent struct {
			Candidate, Run string
		}
		AfterAgent struct {
			Candidate, Run    *string
			Candidates, Runs  []string
			CandidatesLoading bool
		}
		AfterProject struct {
			Agent, Candidate, Run *string
			Agents                []string
			Environments          int
		}
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if got.Before.Agent != "a1" || got.Before.Candidate != "c1" || got.Before.Run != "r1" {
		t.Fatalf("setup did not reach a1/c1/r1: %+v", got.Before)
	}
	if got.SameAgent.Candidate != "c1" || got.SameAgent.Run != "r1" {
		t.Errorf("re-choosing the selected agent cleared its dependents: %+v", got.SameAgent)
	}
	if got.AfterAgent.Candidate != nil || got.AfterAgent.Run != nil {
		t.Errorf("changing agent kept candidate=%v run=%v", got.AfterAgent.Candidate, got.AfterAgent.Run)
	}
	if len(got.AfterAgent.Runs) != 0 || len(got.AfterAgent.Candidates) != 0 {
		t.Errorf("changing agent kept the previous agent's pages: candidates=%v runs=%v",
			got.AfterAgent.Candidates, got.AfterAgent.Runs)
	}
	if !got.AfterAgent.CandidatesLoading {
		t.Error("choosing an agent did not read its candidates")
	}
	if got.AfterProject.Agent != nil || got.AfterProject.Candidate != nil || got.AfterProject.Run != nil {
		t.Errorf("changing project kept a dependent selection: %+v", got.AfterProject)
	}
	if len(got.AfterProject.Agents) != 0 || got.AfterProject.Environments != 0 {
		t.Errorf("changing project kept the previous project's agents %v or environments (%d)",
			got.AfterProject.Agents, got.AfterProject.Environments)
	}
}

// TestContextDiscardsAPageForASupersededParent is the stale-response rule.
func TestContextDiscardsAPageForASupersededParent(t *testing.T) {
	driver := contextHarness + `
const api = deferredAPI();
const ctx = createSelectionContext(api.deps);
const out = {};

ctx.choose("project", { id: "p1" });
ctx.choose("agent", { id: "slow" });
await tick();
ctx.choose("agent", { id: "fast" });
await tick();
// The newer agent's page lands first, then the superseded one.
await api.settle("candidates", "fast", { candidates: [{ id: "fast-c1" }, { id: "fast-c2" }] });
await tick();
out.afterFast = { rows: ids(ctx.pages.candidates), loading: ctx.pages.candidates.loading };
await api.settle("candidates", "slow", { candidates: [{ id: "slow-c1" }] });
await tick();
out.afterSlow = { rows: ids(ctx.pages.candidates), parent: ctx.pages.candidates.parentID,
  candidate: sel(ctx, "candidate") };

// And the other order: the superseded page lands while the current one is
// still out. It must neither appear nor lower the current loading flag.
ctx.choose("agent", { id: "old" });
await tick();
ctx.choose("agent", { id: "new" });
await tick();
await api.settle("candidates", "old", { candidates: [{ id: "old-c1" }] });
await tick();
out.staleFirst = { rows: ids(ctx.pages.candidates), loading: ctx.pages.candidates.loading };
await api.fail("candidates", "new", new Error("boom"));
await tick();
out.currentFailed = { loading: ctx.pages.candidates.loading, error: String(ctx.pages.candidates.error) };
process.stdout.write(JSON.stringify(out));
`
	var got struct {
		AfterFast struct {
			Rows    []string
			Loading bool
		}
		AfterSlow struct {
			Rows      []string
			Parent    string
			Candidate *string
		}
		StaleFirst struct {
			Rows    []string
			Loading bool
		}
		CurrentFailed struct {
			Loading bool
			Error   string
		}
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if len(got.AfterFast.Rows) != 2 || got.AfterFast.Loading {
		t.Fatalf("the current agent's page did not land: %+v", got.AfterFast)
	}
	if got.AfterSlow.Parent != "fast" || len(got.AfterSlow.Rows) != 2 || got.AfterSlow.Rows[0] != "fast-c1" {
		t.Errorf("a superseded agent's candidates replaced the current ones: %+v", got.AfterSlow)
	}
	if got.AfterSlow.Candidate != nil {
		t.Errorf("a superseded page preselected %v", *got.AfterSlow.Candidate)
	}
	if len(got.StaleFirst.Rows) != 0 || !got.StaleFirst.Loading {
		t.Errorf("a stale page landed or cleared the current read's loading flag: %+v", got.StaleFirst)
	}
	if got.CurrentFailed.Loading || got.CurrentFailed.Error != "Error: boom" {
		t.Errorf("the current read's failure was not recorded: %+v", got.CurrentFailed)
	}
}

// TestContextPreselectsOnlyAWholeCollectionOfOne is the "unambiguous" rule.
func TestContextPreselectsOnlyAWholeCollectionOfOne(t *testing.T) {
	driver := contextHarness + `
const out = {};
out.pure = {
  one: unambiguousSingle({ loaded: true, nextAfter: "", rows: [{ id: "x" }] })?.id ?? null,
  oneWithMore: unambiguousSingle({ loaded: true, nextAfter: "x", rows: [{ id: "x" }] }),
  two: unambiguousSingle({ loaded: true, nextAfter: "", rows: [{ id: "x" }, { id: "y" }] }),
  notLoaded: unambiguousSingle({ loaded: false, nextAfter: "", rows: [{ id: "x" }] }),
};

// One agent, one candidate, two runs: agent and candidate are chosen, the
// run is not.
{
  const api = deferredAPI();
  const ctx = createSelectionContext(api.deps);
  const done = ctx.choose("project", { id: "p" });
  await tick();
  await api.settle("agents", "p", { agents: [{ id: "only-agent", name: "Only" }] });
  await api.settle("environments", "p", { environments: [] });
  await tick();
  await api.settle("candidates", "only-agent", { candidates: [{ id: "only-cand", metadata: { label: "v1" } }] });
  await tick();
  await api.settle("runs", "p", { evaluation_runs: [{ id: "r1" }, { id: "r2" }] });
  await done;
  out.chain = { agent: sel(ctx, "agent"), agentLabel: ctx.selection.agent?.label,
    candidate: sel(ctx, "candidate"), candidateLabel: ctx.selection.candidate?.label,
    preselected: ctx.selection.candidate?.preselected, run: sel(ctx, "run") };
}

// One agent on a page that has a continuation: not chosen.
{
  const api = deferredAPI();
  const ctx = createSelectionContext(api.deps);
  const done = ctx.choose("project", { id: "p" });
  await tick();
  await api.settle("agents", "p", { agents: [{ id: "a" }], next_after: "a" });
  await api.settle("environments", "p", { environments: [] });
  await done;
  out.partial = { agent: sel(ctx, "agent"), calls: api.calls.map((c) => c.name) };
}
process.stdout.write(JSON.stringify(out));
`
	var got struct {
		Pure struct {
			One         *string
			OneWithMore *struct{}
			Two         *struct{}
			NotLoaded   *struct{}
		}
		Chain struct {
			Agent, AgentLabel, Candidate, CandidateLabel string
			Preselected                                  bool
			Run                                          *string
		}
		Partial struct {
			Agent *string
			Calls []string
		}
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if got.Pure.One == nil || *got.Pure.One != "x" {
		t.Error("a whole collection of one is not unambiguous")
	}
	if got.Pure.OneWithMore != nil || got.Pure.Two != nil || got.Pure.NotLoaded != nil {
		t.Error("unambiguousSingle guessed from a partial, plural or unloaded page")
	}
	if got.Chain.Agent != "only-agent" || got.Chain.Candidate != "only-cand" {
		t.Errorf("a whole collection of one was not preselected: %+v", got.Chain)
	}
	if got.Chain.AgentLabel != "Only" || got.Chain.CandidateLabel != "v1" || !got.Chain.Preselected {
		t.Errorf("preselection lost the name or did not say it preselected: %+v", got.Chain)
	}
	if got.Chain.Run != nil {
		t.Errorf("one of two runs was preselected: %s", *got.Chain.Run)
	}
	if got.Partial.Agent != nil {
		t.Errorf("the only agent on a page with a continuation was preselected")
	}
	for _, call := range got.Partial.Calls {
		if call == "candidates" {
			t.Error("an agent that was not chosen had its candidates read")
		}
	}
}

// TestContextPasteResolvesTheWholeChain is the advanced path: a pasted run
// lands under the candidate, agent and project it actually belongs to.
func TestContextPasteResolvesTheWholeChain(t *testing.T) {
	driver := contextHarness + `
const api = deferredAPI();
const ctx = createSelectionContext(api.deps);
const out = {};

ctx.assume("project", { id: "other-project" });
ctx.assume("agent", { id: "other-agent" });
const adopting = ctx.adopt("run", "  run-9  ");
await tick();
await api.settle("getRun", "run-9", { id: "run-9", candidate_id: "cand-9" });
await tick();
await api.settle("getCandidate", "cand-9", { id: "cand-9", agent_id: "agent-9", metadata: { label: "nine" } });
await tick();
await api.settle("getAgent", "agent-9", { id: "agent-9", project_id: "proj-9", name: "Agent Nine" });
await tick();
await api.settle("getProject", "proj-9", { id: "proj-9", name: "Project Nine" });
const result = await adopting;
out.ok = result.ok;
out.chain = ["project", "agent", "candidate", "run"].map((l) => sel(ctx, l));
out.labels = ["project", "agent", "candidate"].map((l) => ctx.selection[l].label);

// A paste the reader abandons by choosing something else is discarded.
const stale = ctx.adopt("run", "run-late");
await tick();
ctx.choose("project", { id: "chosen-now" });
await api.settle("getRun", "run-late", { id: "run-late", candidate_id: "c" });
const staleResult = await stale;
out.stale = { ok: staleResult.ok, project: sel(ctx, "project"), run: sel(ctx, "run") };

// An unknown identifier is reported, not adopted.
const missing = ctx.adopt("agent", "nope");
await tick();
await api.fail("getAgent", "nope", Object.assign(new Error("agent not found"), { status: 404 }));
const missingResult = await missing;
out.missing = { ok: missingResult.ok, error: String(missingResult.error), project: sel(ctx, "project") };
process.stdout.write(JSON.stringify(out));
`
	var got struct {
		OK     bool
		Chain  []string
		Labels []string
		Stale  struct {
			OK      bool
			Project string
			Run     *string
		}
		Missing struct {
			OK      bool
			Error   string
			Project string
		}
	}
	decodeDriver(t, runDriver(t, driver), &got)

	want := []string{"proj-9", "agent-9", "cand-9", "run-9"}
	if !got.OK || len(got.Chain) != 4 {
		t.Fatalf("paste did not resolve: %+v", got)
	}
	for i := range want {
		if got.Chain[i] != want[i] {
			t.Errorf("pasted run's chain = %v, want %v", got.Chain, want)
			break
		}
	}
	if got.Labels[0] != "Project Nine" || got.Labels[1] != "Agent Nine" || got.Labels[2] != "nine" {
		t.Errorf("paste lost the names the records carry: %v", got.Labels)
	}
	if got.Stale.OK || got.Stale.Project != "chosen-now" || got.Stale.Run != nil {
		t.Errorf("an abandoned paste overwrote the reader's newer choice: %+v", got.Stale)
	}
	if got.Missing.OK || got.Missing.Error != "Error: agent not found" || got.Missing.Project != "chosen-now" {
		t.Errorf("an unknown identifier was adopted or its error lost: %+v", got.Missing)
	}
}

// TestContextRunPageFollowsTheDeepestScope is task 101 in the context: the
// run list is the recency collection at the deepest chosen level, read only
// when asked for at project and agent scope, and a run chosen from a wide
// list brings its own candidate and agent with it.
func TestContextRunPageFollowsTheDeepestScope(t *testing.T) {
	driver := contextHarness + `
const api = deferredAPI();
const ctx = createSelectionContext(api.deps);
const out = {};

const choosing = ctx.choose("project", { id: "p1", name: "One" });
await api.settle("agents", "p1", { agents: [{ id: "a1" }, { id: "a2" }] });
await api.settle("environments", "p1", { environments: [] });
await choosing;
out.eagerAtProject = api.calls.filter((c) => c.name === "runs").length;
out.scopeAtProject = ctx.runScope;

const ensuring = ctx.ensure("runs");
await tick();
const projectCall = api.find("runs", "p1");
out.projectArgs = projectCall.args.slice(0, 2);
await api.settle("runs", "p1", { evaluation_runs: [{ id: "r9", candidate_id: "c9" }, { id: "r1", candidate_id: "c1" }] });
await ensuring;
out.projectRows = ids(ctx.pages.runs);

// Choosing an agent drops the project-wide page: it was listed for another scope.
ctx.choose("agent", { id: "a2" });
out.afterAgent = { rows: ids(ctx.pages.runs), scope: ctx.runScope.level };
await api.settle("candidates", "a2", { candidates: [{ id: "c9" }, { id: "c8" }] });

// A run chosen from the agent-wide list resolves its candidate from the record.
const ensuring2 = ctx.ensure("runs");
await api.settle("runs", "p1", { evaluation_runs: [{ id: "r9", candidate_id: "c9" }] });
await ensuring2;
const picking = ctx.choose("run", { id: "r9", candidate_id: "c9" });
await api.settle("getRun", "r9", { id: "r9", candidate_id: "c9" });
await api.settle("getCandidate", "c9", { id: "c9", agent_id: "a2", metadata: { label: "nine" } });
await api.settle("getAgent", "a2", { id: "a2", project_id: "p1", name: "Two" });
await api.settle("getProject", "p1", { id: "p1", name: "One" });
await picking;
out.picked = { project: sel(ctx, "project"), agent: sel(ctx, "agent"), candidate: sel(ctx, "candidate"),
  run: sel(ctx, "run"), scope: ctx.runScope };
const candidateRunCalls = api.calls.filter((c) => c.name === "runs" && c.args[1] && c.args[1].candidateID === "c9");
out.candidateScopedReads = candidateRunCalls.length;
process.stdout.write(JSON.stringify(out));
`
	var got struct {
		EagerAtProject int
		ScopeAtProject struct{ Level, Label string }
		ProjectArgs    []any
		ProjectRows    []string
		AfterAgent     struct {
			Rows  []string
			Scope string
		}
		Picked struct {
			Project, Agent, Candidate, Run string
			Scope                          struct{ Level, Label string }
		}
		CandidateScopedReads int
	}
	decodeDriver(t, runDriver(t, driver), &got)

	if got.EagerAtProject != 0 {
		t.Errorf("choosing a project read %d run pages; runs at project scope are read on demand", got.EagerAtProject)
	}
	if got.ScopeAtProject.Level != "project" || got.ScopeAtProject.Label != "One" {
		t.Errorf("runScope at project = %+v", got.ScopeAtProject)
	}
	if len(got.ProjectArgs) != 2 || got.ProjectArgs[0] != "p1" {
		t.Errorf("project-scope read args = %v", got.ProjectArgs)
	}
	if strings.Join(got.ProjectRows, ",") != "r9,r1" {
		t.Errorf("project rows = %v; the server's order is kept, not re-sorted", got.ProjectRows)
	}
	if len(got.AfterAgent.Rows) != 0 || got.AfterAgent.Scope != "agent" {
		t.Errorf("after choosing an agent: %+v; the project-wide page must be dropped", got.AfterAgent)
	}
	if got.Picked.Agent != "a2" || got.Picked.Candidate != "c9" || got.Picked.Run != "r9" ||
		got.Picked.Scope.Level != "candidate" || got.Picked.Scope.Label != "nine" {
		t.Errorf("a run chosen from a wide list did not bring its chain: %+v", got.Picked)
	}
	if got.CandidateScopedReads != 1 {
		t.Errorf("the candidate's run page was read %d times after adoption, want exactly 1", got.CandidateScopedReads)
	}
}
