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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// agentSourceNote says where the agent's identity came from.
//
// Worth a line because the two cases behave differently: an adopted name means
// dev left the workload's own declaration alone, and a flagged one means dev
// exported OTEL_SERVICE_NAME so the two agree.
func agentSourceNote(identity devIdentity) string {
	if identity.AgentFromTelemetry {
		return "   (from the workload's OTEL_SERVICE_NAME)"
	}
	return "   (exported as OTEL_SERVICE_NAME)"
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
  --run-id <id>          default: generated per invocation

State lives outside your repository, under ~/.trustvian/dev/, keyed by this
directory. dev prints the path on every start. Your repository is never
written to.

Exit status is the command's own, so this wrapper is transparent to scripts.
Before the command starts, 2 means the invocation was wrong and 3 means this
wrapper could not start it.

Not yet composed by this build: instrumentation ownership. See
docs/tasks/v1.0/077-unified-otlp-local-dev-runtime.md.`

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
		command:      command,
		apiURL:       *apiURL,
		localBin:     *localBin,
		collectorBin: *collectorBin,
		project:      *project,
		agent:        *agent,
		candidate:    *candidate,
		environment:  *environment,
		runID:        *runID,
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
	workloadDir, err := resolveWorkloadDir()
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(s.err,
			"trustvian dev: cannot determine your home directory, so there is "+
				"nowhere outside your repository to keep state: %v\n", err)
		return exitDevOperational
	}

	stateDir, err := devStateDir(home, workloadDir)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}

	// Snapshotted before anything is set, so slice 4's instrumentation evidence
	// is read from what the developer provided rather than from what dev
	// provided a moment earlier.
	environment := captureEnvironment()

	// Identity is derived before anything starts. A run that cannot be named
	// deterministically should fail before a control plane, a Collector or a
	// workload has been launched — nothing has to be torn down to report it.
	identity, err := deriveIdentity(config, workloadDir, environment, time.Now())
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevUsage
	}
	if err := environment.declareIdentity(identity); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevUsage
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
			return exitDevOperational
		}

		// The control plane starts before the workload, and a failure here must
		// happen before the workload is launched: a workload with nowhere to
		// report its behavior has produced nothing, and starting it anyway would
		// waste a developer's run and look like a Trustvian success.
		runtime, err := startLocalRuntime(binary, stateDir)
		if err != nil {
			fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
			return exitDevOperational
		}
		defer runtime.stop()
		apiURL = runtime.APIURL()
	}

	// The Collector comes after the control plane and before the workload. After,
	// because slice 3 points it at a run that must already exist and be running;
	// before, because a workload that started with nowhere to send telemetry
	// would produce no evidence and look like it had.
	collectorBin, err := helper{
		name:      collectorBinary,
		role:      "the OTLP receiver",
		flagValue: config.collectorBin,
		flagName:  "--collector-bin",
		envVar:    collectorBinaryEnv,
	}.resolve()
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}

	// Provisioning happens before the Collector, because the Collector's
	// configuration names a run that must already exist and be running: ingest is
	// refused for a pending run, and the processor's sink reads its cursor at
	// startup.
	provision, err := newProvisioner(apiURL, identity)
	if err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}

	provisionCtx, cancelProvision := lifecycleContext()
	if err := provision.ensureHierarchy(provisionCtx); err != nil {
		cancelProvision()
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}
	if err := provision.startRun(provisionCtx); err != nil {
		cancelProvision()
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}
	cancelProvision()

	otlp, err := startCollector(collectorBin, stateDir, collectorConfigData{
		APIURL:           apiURL,
		RunID:            identity.Run,
		Profile:          identity.Profile,
		PendingStatePath: filepath.Join(stateDir, collectorPendingStateFile),
	})
	if err != nil {
		// The run was started and no evidence will reach it. Failed rather than
		// left running: a run stuck in `running` forever is indistinguishable
		// from one still in progress.
		failRunAfter(s, provision, exitDevOperational)
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
		return exitDevOperational
	}

	environment.routeOTLP(otlp.OTLPEndpoint(), otlp.OTLPGRPCEndpoint())

	printDevBanner(s, stateDir, apiURL, otlp, environment, identity, config)

	code := superviseChild(s, config.command, environment)

	// The Collector stops *before* the run reaches a terminal state, and the
	// order is not cosmetic: ingest is refused once a run is terminal, so a span
	// still in flight would fail the batch rather than be ignored. Stopping it
	// first also flushes what it holds.
	otlp.stop()

	lifecycleCtx, cancelLifecycle := lifecycleContext()
	defer cancelLifecycle()

	if code == exitDevOK {
		if err := provision.completeRun(lifecycleCtx); err != nil {
			fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
			// The workload succeeded; the bookkeeping did not. Reported as
			// operational rather than as the workload's success, because a run
			// left non-terminal is evidence nobody can read.
			return exitDevOperational
		}
		fmt.Fprintf(s.out, "\nRun %s complete.\n", identity.Run)
		return code
	}

	// A workload that failed produced no verdict, so the run is failed, not
	// completed. Nothing downstream may read it as a finished evaluation.
	if err := provision.failRun(lifecycleCtx, code); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: %v\n", err)
	}
	fmt.Fprintf(s.out, "\nRun %s failed: the workload exited %d.\n", identity.Run, code)
	// The workload's status, unchanged. dev reports what happened to the run and
	// still exits with the child's code, because that is the contract.
	return code
}

// failRunAfter records a failure for a run nothing will report evidence to.
//
// Best effort and never fatal: this is already an error path, and a second error
// here must not replace the first in the message a developer reads.
func failRunAfter(s streams, provision *provisioner, code int) {
	ctx, cancel := lifecycleContext()
	defer cancel()
	if err := provision.failRun(ctx, code); err != nil {
		fmt.Fprintf(s.err, "trustvian dev: could not fail the run: %v\n", err)
	}
}

// printDevBanner says what was composed and where to look.
//
// On dev's own stdout, which is the terminal the workload also writes to. That
// interleaving is expected and is not the guarantee the child-process discipline
// makes: what must not happen is dev *reformatting or buffering* the workload's
// own streams, and it does neither.
func printDevBanner(s streams, stateDir, apiURL string, otlp *collector,
	environment *devEnvironment, identity devIdentity, config devConfig) {
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
	fmt.Fprintf(s.out, "  Set      %s\n", strings.Join(environment.Added(), " "))
	fmt.Fprintf(s.out, "\n  Not yet composed: instrumentation ownership.\n")
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
