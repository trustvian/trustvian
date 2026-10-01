package main

// `trustvian dev` runs one workload under Trustvian.
//
// This slice is the wrapper and nothing else: it launches a command, behaves
// like a well-mannered parent process, and propagates the child's status. The
// control plane, the Collector, provisioning and instrumentation ownership
// arrive in later slices, and the command says so rather than pretending to
// compose a runtime it does not yet start.
//
// The wrapper half is deliberately first. Everything else this command will do
// hangs off a correctly supervised child, and a wrapper that loses an exit code
// or orphans a process is not repairable by adding features to it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// environmentSnapshot captures the inherited environment.
//
// A function rather than one shared value because deriveIdentity must see only
// what the developer provided: dev sets OTEL_* variables, and reading them back as
// evidence would make identity and instrumentation ownership a function of dev
// rather than of the workload.
func environmentSnapshot() *devEnvironment { return captureEnvironment() }

// agentSourceNote says where the agent's identity came from.
//
// Worth a line because the two cases behave differently: an adopted name means
// dev left the workload's own declaration alone, and a flagged one means dev
// exported OTEL_SERVICE_NAME so the two agree.
func agentSourceNote(identity devIdentity) string {
	if identity.AgentFromTelemetry {
		return "   (declared by the workload)"
	}
	if identity.AgentOverrode != "" {
		// dev relabelling a workload's own declared identity is a thing to be
		// told about, not to discover from a diff.
		return fmt.Sprintf("   (--agent overrides the workload's %q)",
			identity.AgentOverrode)
	}
	return "   (exported as OTEL_SERVICE_NAME)"
}

// ownerEvidenceNote says why auto chose a mode.
//
// Only for auto: an explicit mode needs no justification, and printing one would
// imply dev had a choice it did not have.
func ownerEvidenceNote(owner ownership) string {
	if owner.evidence == "" {
		return ""
	}
	return "   (auto: " + owner.evidence + ")"
}

func dirtyNote(identity devIdentity) string {
	if identity.Dirty {
		return "   uncommitted changes"
	}
	return ""
}

// devUsage is printed on a usage error and by --help.
//
// It documents `--` as required rather than optional. See splitDevArgs.
const devUsage = `usage:
  trustvian dev [options] -- <command> [args...]

Runs a command under Trustvian. The command is not modified and gains no
Trustvian dependency: everything is composed around it.

  --                     required separator; everything after it is the command

Options:
  --api-url <url>        attach to a control plane already running, instead of
                         starting one
  --local-bin <path>     path to trustvian-local
  --collector-bin <path> path to trustvian-collector
  -h, --help             print this message

Identity, each derived when not given:
  --project <id>         default: the git repository's name
  --agent <id>           default: the workload's own OTEL_SERVICE_NAME.
                         Required when it declares none — nothing is invented
  --candidate <id>       default: git:<short sha>, with +dirty when the
                         worktree has uncommitted changes
  --environment <ref>    default: local
  --behavioral-profile <ref>
                         learning scope for this run; default: the candidate
  --run-id <id>          default: generated per invocation

Instrumentation ownership:
  --instrumentation <mode>
                         existing   your workload already sends OpenTelemetry;
                                    dev configures OTLP and injects nothing
                         none       dev manages no instrumentation at all
                         auto       (default) resolve from positive evidence,
                                    or stop rather than guess

State lives outside your repository, under ~/.trustvian/dev/, keyed by this
directory. dev prints the path on every start. Your repository is never
written to.

Exit status is the command's own, so this wrapper is transparent to scripts.
Before the command starts, 2 means the invocation was wrong and 3 means this
wrapper could not start it.

Absence of detectable instrumentation never selects injection: a workload that
initializes OpenTelemetry a moment after it starts cannot be detected
beforehand, and attaching a second stack would report every action twice.`

// runDev is the `dev` family's entry point.
//
// Returns the process exit code. It never returns an error for the caller to
// map, because this command's exit status is the child's and must not pass
// through main's error -> 1 mapping: 1 is a legitimate exit code for a
// workload, and `eval compare` already gives it a meaning of its own.
func runDev(s streams, args []string) int {
	// Platform support is settled before anything else happens. On a platform
	// where the child cannot be supervised correctly, the honest answer is to
	// refuse rather than to start something that cannot be stopped.
	if err := devPlatformSupported(); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevUsage
	}

	before, command, ok := splitDevArgs(args)
	if !ok {
		// --help is the one case where a missing -- is not a mistake.
		if hasHelpFlag(before) {
			fmt.Fprintln(s.out, devUsage)
			return exitDevOK
		}
		fmt.Fprintf(s.err,
			"trustvian dev: a -- separator is required, so the command is "+
				"unambiguous\n\n%s\n", devUsage)
		return exitDevUsage
	}
	if hasHelpFlag(before) {
		fmt.Fprintln(s.out, devUsage)
		return exitDevOK
	}

	fs := newFlagSet("dev")
	apiURL := fs.String("api-url", "",
		"attach to a control plane already running instead of starting one")
	localBin := fs.String("local-bin", "", "path to "+localRuntimeBinary)
	collectorBin := fs.String("collector-bin", "", "path to "+collectorBinary)
	project := fs.String("project", "", "project identifier (default: the repository name)")
	agent := fs.String("agent", "",
		"agent identifier (default: the workload's own OTEL_SERVICE_NAME)")
	candidate := fs.String("candidate", "",
		"candidate identifier (default: git:<short sha>, +dirty when uncommitted)")
	environment := fs.String("environment", "",
		"environment ref (default: "+devEnvironmentDefault+")")
	runID := fs.String("run-id", "", "evaluation run identifier (default: generated per run)")
	instrumentation := fs.String("instrumentation", string(modeAuto),
		"who owns OpenTelemetry setup: existing, none or auto")
	behavioralProfile := fs.String("behavioral-profile", "",
		"learning scope for this run (default: the candidate)")
	if err := fs.Parse(before); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n\n%s\n", err, devUsage)
		return exitDevUsage
	}
	// Positional arguments before -- are always a mistake: this command takes
	// its own input through flags and the workload's through --. Accepting a
	// stray one silently is how `trustvian dev python agent.py` would run
	// something nobody asked for.
	if fs.NArg() != 0 {
		fmt.Fprintf(s.err,
			"trustvian dev: unexpected argument %q before --; the command goes "+
				"after --\n\n%s\n", fs.Arg(0), devUsage)
		return exitDevUsage
	}

	if len(command) == 0 {
		fmt.Fprintf(s.err,
			"trustvian dev: no command after --\n\n%s\n", devUsage)
		return exitDevUsage
	}

	return composeAndRun(s, devConfig{
		command:         command,
		apiURL:          *apiURL,
		localBin:        *localBin,
		collectorBin:    *collectorBin,
		project:         *project,
		agent:           *agent,
		candidate:       *candidate,
		environment:     *environment,
		runID:           *runID,
		instrumentation: *instrumentation,

		behavioralProfile: *behavioralProfile,
	})
}

// devConfig is one invocation's resolved inputs.
//
// A struct rather than a long parameter list, because later slices add
// identity, instrumentation mode and gate limits to it and a growing signature
// is how a supervisor acquires arguments nobody can order correctly.
type devConfig struct {
	command      []string
	apiURL       string
	localBin     string
	collectorBin string

	// Identity overrides. Empty means "derive it"; see dev_identity.go.
	project     string
	agent       string
	candidate   string
	environment string
	runID       string

	// behavioralProfile overrides the learning scope, which otherwise is the
	// candidate. Task 078: a scenario runner needs a profile per repetition,
	// and allocating a candidate per repetition to get one would change the
	// identity of the thing under test to obtain an isolation property that has
	// nothing to do with identity.
	behavioralProfile string

	// instrumentation is the ownership mode, as given. Parsed in composeAndRun so
	// an unknown value is a usage error before anything starts.
	instrumentation string

	// env adds variables to the inherited environment, as if the developer had
	// exported them. Task 078: a scenario declares each side's variables in its
	// own file, read from the developer's own repository, so they belong on the
	// trusted side of the snapshot — and passing them here rather than through
	// os.Setenv keeps one repetition's variables out of the next. The dev command
	// line never sets this.
	env map[string]string

	// deadline, when set, bounds the whole invocation: composition, the
	// workload and the run's finalization (task 078 suites). When it is done
	// the workload's process group is terminated — SIGTERM, then SIGKILL after
	// deadlineTerminationGrace — and the run is failed, never completed,
	// whatever the workload exits with. The dev command line never sets this.
	deadline context.Context

	// callerOwnsSignals means the caller — a suite — owns SIGINT and SIGTERM
	// for the whole process, so this session does not subscribe to them. The
	// caller's cancellation reaches the session through deadline, which
	// terminates the workload's process group once: one owner, one path, no
	// second signal from a second subscriber. The dev command line never sets
	// this.
	callerOwnsSignals bool

	// detachStdin gives the workload no standard input and never the terminal:
	// an unattended suite member reading the terminal would stop, and a
	// workload in the terminal's foreground would receive the terminal's
	// Ctrl-C directly, beside the suite's own stop. The dev command line
	// never sets this.
	detachStdin bool

	// workloadStdout, when set, receives the workload's standard output instead
	// of dev's own. Task 078: `eval run` sends it to its stderr, so its stdout
	// carries one result document and nothing else. Per invocation, never by
	// changing this process's descriptors. The dev command line never sets this.
	workloadStdout *os.File
}

// devResult is how one invocation ended: the status dev exits with, and whether
// the run it started reached `completed`.
//
// Two facts, because dev's exit status is the workload's, unmodified, once the
// workload has started (docs/compatibility.md) — a successful workload whose run
// could not be completed still exits 0. `trustvian dev` publishes only the
// first. `eval run` needs the second: a repetition the control plane does not
// hold as completed is not a successful repetition, whatever the workload did.
type devResult struct {
	code         int
	runCompleted bool
}

// inheritedEnvironment is the developer's environment for this invocation: the
// process environment, plus any variables the configuration adds.
func (c devConfig) inheritedEnvironment() *devEnvironment {
	environment := environmentSnapshot()
	for name, value := range c.env {
		environment.snapshot[name] = value
	}
	return environment
}

// composeAndRun brings up what the workload needs, runs it, and tears down.
//
// The order is the contract, and it is the order scripts/tv-dev.sh in the demo
// repository arrived at by running into each failure:
//
//	state directory -> control plane -> (OTLP path, slice 2b)
//	  -> workload -> teardown in reverse
//
// Teardown is deferred immediately after each successful start, so a failure
// half-way leaves nothing running. That is what "nothing it started is left"
// means when there is more than one thing to start.
func composeAndRun(s streams, config devConfig) int {
	return composeAndRunResult(s, config).code
}

// composeAndRunResult is composeAndRun, also reporting whether the run was
// completed.
func composeAndRunResult(s streams, config devConfig) devResult {
	// Signals first, before anything exists to orphan.
	//
	// The first version installed the handler inside superviseChild, which is
	// after the control plane and the Collector have started — so a Ctrl-C during
	// composition killed dev with the default disposition and left two helpers
	// running. Each is in its own process group, so the terminal's own SIGINT
	// never reached them: the developer got their shell back and two orphans
	// holding ports.
	relay := newSignalRelayOwning(!config.callerOwnsSignals)
	// Stopped after teardown, not before: shutting down still has work to do, and
	// a second Ctrl-C during it should reach the helpers rather than killing dev.
	defer relay.Stop()
	// Registered after Stop, so it runs first: the watcher ends before the
	// relay it would expire.
	defer relay.watchDeadline(config.deadline, deadlineTerminationGrace)()

	workloadDir, err := resolveWorkloadDir()
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return devResult{code: exitDevOperational}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(s.err,
			"trustvian dev: cannot determine your home directory, so there is "+
				"nowhere outside your repository to keep state: %v\n", err)
		return devResult{code: exitDevOperational}
	}

	stateDir, err := devStateDir(home, workloadDir)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return devResult{code: exitDevOperational}
	}

	// Identity before any process starts. A run that cannot be named
	// deterministically should fail before a control plane, a Collector or a
	// workload has been launched — nothing has to be torn down to report it.
	identity, err := deriveIdentity(config, workloadDir, config.inheritedEnvironment(), time.Now())
	if config.deadline != nil && config.deadline.Err() != nil {
		// The deadline passed while the repository was being inspected. The
		// inspection stopped early, so whatever identity it produced is not
		// one to provision — and nothing has started.
		fmt.Fprintf(s.err, "trustvian dev: the deadline passed before the run could be named\n")
		return devResult{code: exitDevOperational}
	}
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return devResult{code: exitDevUsage}
	}
	environment := config.inheritedEnvironment()
	if err := environment.declareIdentity(identity); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return devResult{code: exitDevUsage}
	}

	mode, err := parseInstrumentationMode(config.instrumentation)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n\n%s\n", err, devUsage)
		return devResult{code: exitDevUsage}
	}
	// Ownership is resolved before *anything* is started or provisioned. A
	// refusal then leaves nothing to tear down and no run to fail — which is the
	// difference between "nothing was launched" and a half-composed runtime with
	// an evaluation run stuck in it.
	owner, err := resolveOwnership(mode, environment, config.command)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return devResult{code: exitDevUsage}
	}

	// The engine's learned baseline is a file, and a file store has no
	// cross-process locking — so two runs writing one baseline would corrupt it.
	// Taken before anything starts, released on the way out.
	baseline, err := acquireBaseline(stateDir, identity.Profile)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return devResult{code: exitDevOperational}
	}
	defer baseline.release()

	session := &devSession{s: s, relay: relay, identity: identity, config: config}
	defer session.teardown()

	if code, done := session.interrupted(); done {
		return devResult{code: code}
	}

	apiURL := config.apiURL
	if apiURL == "" {
		binary, err := helper{
			name:      localRuntimeBinary,
			role:      "the local control plane",
			flagValue: config.localBin,
			flagName:  "--local-bin",
			envVar:    localRuntimeBinaryEnv,
		}.resolve()
		if err != nil {
			fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
			return devResult{code: exitDevOperational}
		}

		// The control plane starts before the workload, and a failure here must
		// happen before the workload is launched: a workload with nowhere to
		// report its behavior has produced nothing, and starting it anyway would
		// waste a developer's run and look like a Trustvian success.
		runtime, err := startLocalRuntime(relay.Context(), binary, stateDir)
		if err != nil {
			return devResult{code: session.fail(err)}
		}
		session.runtime = runtime
		apiURL = runtime.APIURL()
	}

	if code, done := session.interrupted(); done {
		return devResult{code: code}
	}

	// Provisioning happens before the Collector, because the Collector's
	// configuration names a run that must already exist and be running: ingest is
	// refused for a pending run, and the processor's sink reads its cursor at
	// startup.
	provision, err := newProvisioner(apiURL, identity)
	if err != nil {
		return devResult{code: session.fail(err)}
	}
	session.provision = provision

	if err := provision.ensureHierarchy(relay.Context()); err != nil {
		return devResult{code: session.fail(err)}
	}
	if err := provision.startRun(relay.Context()); err != nil {
		return devResult{code: session.fail(err)}
	}
	// From here on a failure has to leave the run terminal: one stuck in
	// `running` forever is indistinguishable from one still in progress.
	session.runStarted = true

	if code, done := session.interrupted(); done {
		return devResult{code: code}
	}

	collectorBin, err := helper{
		name:      collectorBinary,
		role:      "the OTLP receiver",
		flagValue: config.collectorBin,
		flagName:  "--collector-bin",
		envVar:    collectorBinaryEnv,
	}.resolve()
	if err != nil {
		return devResult{code: session.fail(err)}
	}

	otlp, err := startCollector(relay.Context(), collectorBin, stateDir, collectorConfigData{
		APIURL:           apiURL,
		RunID:            identity.Run,
		Profile:          identity.Profile,
		PendingStatePath: filepath.Join(stateDir, collectorPendingStateFile),
		BaselinePath:     baseline.path,
	})
	if err != nil {
		return devResult{code: session.fail(err)}
	}
	session.collector = otlp

	if code, done := session.interrupted(); done {
		return devResult{code: code}
	}

	// `none` means dev manages no instrumentation, which includes not routing:
	// the workload's telemetry reaches the Collector some other way, and dev
	// setting OTEL_* would be managing the thing it was told not to.
	if owner.routesOTLP() {
		environment.routeOTLP(otlp.OTLPEndpoint(), otlp.OTLPGRPCEndpoint())
	}

	printDevBanner(s, stateDir, apiURL, otlp, environment, identity, baseline, owner, config)

	outcome := superviseChildWith(s, config.command, environment, relay, childStreams{
		stdout: config.workloadStdout, detachStdin: config.detachStdin,
	})

	// The Collector stops *before* the run reaches a terminal state, and the
	// order is not cosmetic: ingest is refused once a run is terminal, so a span
	// still in flight would fail the batch rather than be ignored. Stopping it
	// first also flushes what it holds.
	otlp.stop()
	session.collector = nil

	code := session.finish(outcome)
	return devResult{code: code, runCompleted: session.runCompleted}
}

// devSession owns what one invocation started, and how it ends.
//
// Teardown is the reason it exists. Four things can be running by the time the
// workload does, and every failure path after the first of them has to stop them
// all, in the reverse order, and leave the run in a terminal state. Doing that
// with deferred closures at each site produced the orphan bug this type replaces.
type devSession struct {
	s        streams
	relay    *signalRelay
	identity devIdentity
	config   devConfig

	runtime    *localRuntime
	collector  *collector
	provision  *provisioner
	runStarted bool
	// runCompleted is set only when the control plane accepted the run's
	// completion.
	runCompleted bool
}

// interrupted reports whether a signal arrived, and what to exit with.
//
// Checked between composition steps. Each step's own wait is already cancelled by
// the relay's context, so this catches the gap between them — where a signal would
// otherwise be noticed only after the next thing had been started.
func (d *devSession) interrupted() (int, bool) {
	if d.relay.Expired() {
		fmt.Fprintf(d.s.err, "trustvian dev: the deadline passed before the workload finished\n")
		d.failRunIfStarted("the deadline passed before the workload finished")
		return exitDevOperational, true
	}
	sig := d.relay.Received()
	if sig == 0 {
		return 0, false
	}
	fmt.Fprintf(d.s.out, "\nInterrupted.\n")
	d.failRunIfStarted("interrupted by the developer before the workload finished")
	return signalExitStatus(sig), true
}

// fail reports a composition failure and exits operationally.
//
// A signal that arrived during the failing step is reported as an interruption
// rather than as the failure it caused, because that is what happened: cancelling
// a wait makes it fail, and calling that an error would blame dev for doing what
// it was asked.
func (d *devSession) fail(err error) int {
	if d.relay.Expired() {
		fmt.Fprintf(d.s.err, "trustvian dev: the deadline passed before the workload finished\n")
		d.failRunIfStarted("the deadline passed before the workload finished")
		return exitDevOperational
	}
	if sig := d.relay.Received(); sig != 0 {
		fmt.Fprintf(d.s.out, "\nInterrupted.\n")
		d.failRunIfStarted("interrupted by the developer before the workload finished")
		return signalExitStatus(sig)
	}
	fmt.Fprintf(d.s.err, "trustvian dev: %v\n", err)
	d.failRunIfStarted("trustvian dev could not finish composing the runtime")
	return exitDevOperational
}

// finish ends the run according to what the workload did, and returns its status.
func (d *devSession) finish(outcome childOutcome) int {
	ctx, cancel := lifecycleContext()
	defer cancel()

	switch {
	case d.relay.Expired() || d.deadlineDone():
		// deadlineDone as well as Expired: a suite cancellation reaches the
		// deadline context and the relay through two goroutines, and the
		// workload can be reaped before the watcher has marked the relay.
		// The deadline ended the workload. Whatever it exited with — a
		// workload that traps SIGTERM can exit 0 — it did not finish, so the
		// run is failed and nothing downstream reads it as evidence.
		d.failRunWith(ctx, "the deadline passed before the workload finished")
		fmt.Fprintf(d.s.out, "\nRun %s failed: the deadline passed.\n", d.identity.Run)
		if outcome.code == exitDevOK {
			return exitDevOperational
		}
		return outcome.code

	case outcome.startFailed:
		// No workload ran, so the reason must not claim a workload status. An
		// earlier version recorded "the workload exited 3", which is the code dev
		// chose for its own failure and not anything the workload did.
		d.failRunWith(ctx, "the workload could not be started under trustvian dev")
		return outcome.code

	case outcome.ok() && d.config.deadline != nil:
		// A suite member's workload succeeded; the run is completed only
		// within the scenario's deadline. finalize says how it ended.
		return d.finalizeWithinDeadline(outcome)

	case d.config.deadline != nil && outcome.stoppedByDeveloper(d.relay.Forwarded()):
		// In a suite a forwarded signal is the suite being cancelled, not a
		// developer ending an interactive run: the run did not finish, so it
		// is failed rather than completed, even if the cancellation has not
		// reached the deadline context yet.
		d.failRunWith(ctx, "the suite was cancelled before the workload finished")
		fmt.Fprintf(d.s.out, "\nRun %s failed: the suite was cancelled.\n", d.identity.Run)
		return outcome.code

	case outcome.stoppedByDeveloper(d.relay.Forwarded()):
		// The developer stopped it. A run ended by Ctrl-C is not a behavioral
		// finding and not a crash: the evidence collected up to that point is
		// real, so the run is completed and the banner says who ended it.
		//
		// Forwarded() alone was not enough. With the terminal handed over, the
		// terminal delivers SIGINT to the workload's group and dev never sees it —
		// so an interactive workload the developer stopped was being recorded as
		// failed. stoppedByDeveloper knows about both routes.
		if err := d.completeRun(ctx); err != nil {
			return outcome.code
		}
		fmt.Fprintf(d.s.out, "\nRun %s completed: stopped by the developer.\n",
			d.identity.Run)
		return outcome.code

	case outcome.ok():
		if err := d.completeRun(ctx); err != nil {
			return outcome.code
		}
		fmt.Fprintf(d.s.out, "\nRun %s complete.\n", d.identity.Run)
		return outcome.code

	default:
		// A workload that failed produced no verdict, so the run is failed, not
		// completed. Nothing downstream may read it as a finished evaluation.
		d.failRunWith(ctx,
			fmt.Sprintf("the workload exited %d under trustvian dev", outcome.code))
		fmt.Fprintf(d.s.out, "\nRun %s failed: the workload exited %d.\n",
			d.identity.Run, outcome.code)
		return outcome.code
	}
}

// deadlineDone reports whether the invocation's deadline context has ended —
// expired, or cancelled with its suite. Always false for `trustvian dev`
// itself, which has none.
func (d *devSession) deadlineDone() bool {
	return d.config.deadline != nil && d.config.deadline.Err() != nil
}

// finalizeWithinDeadline completes a suite member's run, bounded by the
// scenario's deadline as well as the usual request timeout.
//
// The completion request is cancelled when the deadline passes or the suite
// is cancelled, so a stalled control plane cannot complete the run after the
// scenario has already failed. A completion that did not finish within the
// deadline is not reported as one: the run is failed under a fresh, bounded
// context — the deadline's own is already done — and the status is
// operational. Should the server have committed the completion just as the
// request was cancelled, the fail is refused and said on stderr; the
// execution is failed either way, so the run is never read as a reference.
func (d *devSession) finalizeWithinDeadline(outcome childOutcome) int {
	ctx, cancel := context.WithTimeout(d.config.deadline, provisionTimeout)
	err := d.completeRun(ctx)
	cancel()
	if err == nil && !d.deadlineDone() {
		fmt.Fprintf(d.s.out, "\nRun %s complete.\n", d.identity.Run)
		return outcome.code
	}
	d.runCompleted = false
	if d.deadlineDone() {
		cleanup, done := lifecycleContext()
		defer done()
		if d.provision != nil {
			if err := d.provision.failRunReason(cleanup,
				"the deadline passed before the run could be completed"); err != nil {
				fmt.Fprintf(d.s.err, "trustvian dev: could not fail run %s: %v\n", d.identity.Run, err)
			}
		}
		fmt.Fprintf(d.s.out, "\nRun %s failed: the deadline passed during finalization.\n",
			d.identity.Run)
	}
	return exitDevOperational
}

// completeRun records success, and never changes the workload's status.
//
// docs/compatibility.md says dev's exit status is the workload's, unmodified,
// once the workload has started. An earlier version returned 3 here when the
// bookkeeping failed after a successful workload, which contradicted that. So the
// failure is reported loudly and named — a run left non-terminal is evidence
// nobody can read — and the status stays the workload's.
func (d *devSession) completeRun(ctx context.Context) error {
	if d.provision == nil {
		return nil
	}
	err := d.provision.completeRun(ctx)
	d.runCompleted = err == nil
	if err != nil {
		fmt.Fprintf(d.s.err,
			"trustvian dev: WARNING: the workload succeeded but run %s could not be "+
				"completed: %v\n"+
				"  The run is left non-terminal and nothing downstream can read it as "+
				"evidence.\n"+
				"  Complete it by hand with: trustvian eval complete --id %s\n",
			d.identity.Run, err, d.identity.Run)
	}
	d.runStarted = false
	return err
}

// failRunIfStarted fails the run when one was started, and says nothing otherwise.
func (d *devSession) failRunIfStarted(reason string) {
	if !d.runStarted {
		return
	}
	ctx, cancel := lifecycleContext()
	defer cancel()
	d.failRunWith(ctx, reason)
}

func (d *devSession) failRunWith(ctx context.Context, reason string) {
	if d.provision == nil || !d.runStarted {
		return
	}
	if err := d.provision.failRunReason(ctx, reason); err != nil {
		fmt.Fprintf(d.s.err, "trustvian dev: could not fail run %s: %v\n",
			d.identity.Run, err)
	}
	d.runStarted = false
}

// teardown stops what this session started, in reverse order.
//
// The Collector before the control plane: the Collector posts evidence to it, so
// the thing that writes stops before the thing written to. Idempotent, because the
// normal path stops the Collector itself and this still runs.
func (d *devSession) teardown() {
	if d.collector != nil {
		d.collector.stop()
		d.collector = nil
	}
	if d.runtime != nil {
		d.runtime.stop()
		d.runtime = nil
	}
}

// printDevBanner says what was composed and where to look.
//
// On dev's own stdout, which is the terminal the workload also writes to. That
// interleaving is expected and is not the guarantee the child-process discipline
// makes: what must not happen is dev *reformatting or buffering* the workload's
// own streams, and it does neither.
func printDevBanner(s streams, stateDir, apiURL string, otlp *collector,
	environment *devEnvironment, identity devIdentity, baseline *baselineLock,
	owner ownership, config devConfig) {
	fmt.Fprintf(s.out, "Trustvian dev\n\n")
	fmt.Fprintf(s.out, "  Project     %s\n", identity.Project)
	fmt.Fprintf(s.out, "  Agent       %s%s\n", identity.Agent,
		agentSourceNote(identity))
	// The dirty marker is on the candidate itself, and said again in words:
	// evaluating uncommitted work is normal, and a developer should not have to
	// notice a suffix to know they are doing it.
	fmt.Fprintf(s.out, "  Candidate   %s%s\n", identity.Candidate,
		dirtyNote(identity))
	fmt.Fprintf(s.out, "  Environment %s\n", identity.Environment)
	fmt.Fprintf(s.out, "  Run         %s\n\n", identity.Run)
	fmt.Fprintf(s.out, "  State    %s\n", stateDir)
	fmt.Fprintf(s.out, "  Baseline %s\n", baseline.path)
	fmt.Fprintf(s.out, "  API      %s\n", apiURL)
	fmt.Fprintf(s.out, "  OTLP     %s  (gRPC %s)\n",
		otlp.OTLPEndpoint(), otlp.OTLPGRPCEndpoint())
	// The browser URL is its own line rather than left to be inferred from the
	// API endpoint: a developer looking for somewhere to click should not have
	// to work out that one is also the other.
	fmt.Fprintf(s.out, "  Web      %s/\n", apiURL)
	if config.apiURL != "" {
		fmt.Fprintf(s.out, "  Attached to a control plane this command did not start.\n")
	}
	// The variables the workload gained are named, not summarized. A developer
	// debugging why their agent emitted nothing needs to know exactly what was
	// set, and a wrapper that changes an environment silently is one they cannot
	// reason about.
	fmt.Fprintf(s.out, "  Owner    %s%s\n", owner.mode, ownerEvidenceNote(owner))
	fmt.Fprintf(s.out, "  Set      %s\n", strings.Join(environment.Added(), " "))
	if !owner.routesOTLP() {
		// "dev manages no instrumentation" means no *routing*. It does not mean no
		// variables: identity is still declared, because identity is what makes the
		// evidence belong to this run rather than to nothing.
		//
		// An earlier version of this line said "nothing", which was simply false —
		// and false in the direction that costs a developer an afternoon, since a
		// workload whose records are all refused looks like a workload emitting
		// none. So the two names are printed above like any other, and what they
		// are for is said here.
		fmt.Fprintf(s.out, "           identity only — no OTLP routing.\n")
		fmt.Fprintf(s.out, "           %s must match this run or the platform\n",
			deploymentEnvironmentKey)
		fmt.Fprintf(s.out, "           refuses every record; %s is the actor the run\n",
			serviceNameKey)
		fmt.Fprintf(s.out, "           is about. Your telemetry must reach the OTLP endpoint\n")
		fmt.Fprintf(s.out, "           above by its own route.\n")
	}
	fmt.Fprintf(s.out, "\nRunning %s\n\n", devCommandName(config.command))
}

// splitDevArgs divides this command's own arguments from the workload's.
//
// The split is done here rather than by flag.Parse, which also stops at --,
// because the boundary has to be *required*. Without it,
//
//	trustvian dev python agent.py --json
//
// is ambiguous: --json could be the workload's or a future flag of this
// command's, and the answer would change the day such a flag is added. A
// required separator makes every existing invocation keep meaning what it
// meant.
//
// Only the first -- is consumed. A second one belongs to the workload and is
// passed through, because `sh -c ... -- --flag` is a legitimate thing to run.
func splitDevArgs(args []string) (before, command []string, ok bool) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

// hasHelpFlag reports whether help was asked for before the separator.
//
// Checked separately from flag parsing so `trustvian dev --help` works without
// a -- the user has no reason to type when they are asking what to type.
func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return true
		}
		// Anything after the first non-flag token is not ours to interpret.
		if !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return false
}

// Exit codes for `dev`.
//
// This command is a **documented exception** to the control-plane families:
// its exit status is the child's, whatever that is, because a wrapper that
// rewrote its child's status would be unusable in a script. See
// docs/compatibility.md § CLI.
//
// The two codes below therefore only apply *before* the child starts. Once it
// has started, its status is the answer — including 2 and 3, which then mean
// whatever the workload means by them. That ambiguity is inherent to wrapping
// and is the same one `env`, `nice` and `timeout` carry; the alternative is
// discarding the child's status, which is worse.
const (
	exitDevOK    = 0
	exitDevUsage = 2
	// exitDevOperational reports that this wrapper could not start the child
	// at all — a command that does not exist, or one that cannot be executed.
	exitDevOperational = 3
)

// deadlineTerminationGrace is how long a workload has, once a deadline has
// passed, between SIGTERM to its process group and SIGKILL.
const deadlineTerminationGrace = 5 * time.Second

// devCommandName renders a command for a diagnostic.
//
// Only the base name of argv[0] plus the argument count: a full command line
// can carry a token, a DSN or a fixture path, and an error message is not a
// place to widen what this tool reveals.
func devCommandName(command []string) string {
	if len(command) == 0 {
		return "(none)"
	}
	name := filepath.Base(command[0])
	if len(command) == 1 {
		return name
	}
	return fmt.Sprintf("%s (+%d arguments)", name, len(command)-1)
}

// devStdio is the child's standard streams.
//
// os.Stdin/Stdout/Stderr directly, never a pipe: the child's output must not be
// captured, interleaved or reformatted, and a pipe would make this process
// responsible for relaying it — which is where wrappers introduce buffering
// that reorders a workload's own output.
//
// A function rather than a constant so tests can substitute files and still
// exercise the real inheritance path.
var devStdio = func() (stdin, stdout, stderr *os.File) {
	return os.Stdin, os.Stdout, os.Stderr
}
