package main

// Deriving the platform identity `trustvian dev` provisions.
//
// Provisioning is only acceptable if identity is **deterministic**: the same
// repository at the same commit must produce the same Project, Agent and
// Candidate across runs. Otherwise every run looks like a brand-new actor, the
// baseline learns nothing, and a diff between two runs of unchanged code reports
// that everything is new — which is the failure task 051 kept candidate metadata
// out of fingerprint identity to prevent, arriving one layer up.
//
// Two rules matter more than the derivation itself.
//
// **No ephemeral value may become behavioral identity.** Not a pid, a port, a
// timestamp or a random token. A candidate is a version, and a version that
// changes every run teaches nothing. A structural test asserts this.
//
// **Nothing is invented.** Where identity cannot be established, dev says so and
// names the flag that fixes it. A fabricated agent name pollutes a durable
// hierarchy and is indistinguishable afterwards from a real one.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// devIdentity is one invocation's platform identity.
type devIdentity struct {
	Project     string
	ProjectName string
	Agent       string
	AgentName   string
	Candidate   string
	Environment string
	Run         string

	// Profile is the learning scope this run's evidence trains.
	//
	// The candidate, not the run. Two runs of one candidate should share a
	// learned baseline — that is what makes "this behavior is new" mean
	// anything on the second run. A profile per run would start every run from
	// an empty baseline, where everything is novel and novelty says nothing.
	//
	// Task 078 owns the opposite requirement: its repetitions must not learn
	// from each other, so it allocates a profile per repetition. Both are
	// correct for what they measure, and neither is a default the other
	// inherits.
	Profile string

	// AgentFromTelemetry records that the agent name came from an inherited
	// OTEL_SERVICE_NAME rather than from a flag, so dev knows not to override
	// what the workload already declares.
	AgentFromTelemetry bool

	// Dirty records an uncommitted worktree, for the banner.
	Dirty bool
}

// devEnvironmentDefault is the environment ref dev provisions under.
//
// "local" because that is what this runtime is. It is a caller-owned ref rather
// than an enum (ADR 0039), so nothing in the platform constrains the choice; the
// value only has to agree with what the telemetry reports, which is why dev also
// sets deployment.environment.name to it.
const devEnvironmentDefault = "local"

// deriveIdentity resolves identity from flags, git and the inherited
// environment.
//
// The inherited environment, not the current one: dev sets OTEL_SERVICE_NAME
// itself when the workload declares none, and reading back its own value would
// make the agent name a function of dev rather than of the workload.
func deriveIdentity(config devConfig, workloadDir string,
	environment *devEnvironment, now time.Time) (devIdentity, error) {
	repository := inspectRepository(workloadDir)

	identity := devIdentity{Environment: devEnvironmentDefault}
	if config.environment != "" {
		identity.Environment = config.environment
	}

	// Project: the repository, else the directory. Both are stable for a given
	// checkout, which is all this needs to be.
	identity.Project = config.project
	if identity.Project == "" {
		identity.Project = repository.projectName(workloadDir)
	}
	identity.ProjectName = identity.Project

	// Agent: the workload's own declared service name wins.
	//
	// The processor derives Actor.ID from the resource attribute service.name,
	// so an Agent that does not match it describes an agent no telemetry ever
	// mentions. When the workload declares one, dev adopts it rather than
	// relabelling somebody else's telemetry.
	if serviceName, ok := environment.Inherited(envServiceName); ok && serviceName != "" {
		identity.Agent = serviceName
		identity.AgentFromTelemetry = true
	}
	if config.agent != "" {
		// An explicit flag wins over the inherited value, and dev then exports
		// it so the two cannot disagree.
		identity.Agent = config.agent
		identity.AgentFromTelemetry = false
	}
	if identity.Agent == "" {
		return devIdentity{}, errNoAgentIdentity
	}
	identity.AgentName = identity.Agent

	// Candidate: the commit, plus a marker when the worktree is dirty.
	identity.Candidate = config.candidate
	if identity.Candidate == "" {
		if !repository.isRepository {
			return devIdentity{}, errNoCandidateIdentity
		}
		if repository.shortSHA == "" {
			return devIdentity{}, errNoCommit
		}
		identity.Candidate = "git:" + repository.shortSHA
		if repository.dirty {
			// Evaluating uncommitted work is normal and useful; silently
			// attributing it to a clean commit is not.
			//
			// Two different dirty states share one candidate id, deliberately: a
			// content hash of the worktree would change on every keystroke and
			// make every save a new candidate, which is the ephemeral identity
			// this file exists to refuse.
			identity.Candidate += "+dirty"
		}
	}
	identity.Dirty = repository.dirty

	identity.Profile = identity.Candidate

	// The run is the one generated value, and the only one allowed to be.
	//
	// ADR 0033 §5 says the CLI generates nothing, and that rule is about the
	// entities a caller names — a project or a candidate a script must be able
	// to address again. A run is per invocation by definition: task 077's
	// identity table says "generated per run", and two invocations sharing a run
	// id would merge two executions' evidence into one.
	//
	// It carries a timestamp, which is ephemeral. That is correct here and
	// nowhere else: EvaluationRunID is correlation and must never become
	// fingerprint identity (ADR 0022), so nothing about it reaches behavioral
	// identity.
	identity.Run = config.runID
	if identity.Run == "" {
		identity.Run = fmt.Sprintf("dev-%s-%s",
			sanitizeRunSegment(identity.Candidate), now.UTC().Format("20060102T150405Z"))
	}

	return identity, validateIdentity(identity)
}

// Identity failures, each naming the flag that resolves it.
var (
	errNoAgentIdentity = errors.New(
		"cannot determine which agent this is.\n\n" +
			"dev takes the agent's identity from the workload's own OTEL_SERVICE_NAME,\n" +
			"because that is what Trustvian derives the actor from. This environment\n" +
			"declares none, so there is nothing to match telemetry against.\n\n" +
			"Name it explicitly:\n\n" +
			"  trustvian dev --agent <name> -- <command>\n\n" +
			"dev will export OTEL_SERVICE_NAME=<name> to the workload, so the agent it\n" +
			"provisions and the actor it observes cannot disagree.")

	errNoCandidateIdentity = errors.New(
		"cannot determine which version of the agent this is.\n\n" +
			"dev derives the candidate from the git commit, and this directory is not a\n" +
			"git repository. A candidate identifier that changed every run would teach\n" +
			"the baseline nothing, so nothing is invented.\n\n" +
			"Name it explicitly:\n\n" +
			"  trustvian dev --candidate <id> -- <command>")

	errNoCommit = errors.New(
		"this git repository has no commits yet, so there is no candidate to\n" +
			"derive. Commit, or name one explicitly:\n\n" +
			"  trustvian dev --candidate <id> -- <command>")
)

// envServiceName is the resource variable the processor reads the actor from.
const envServiceName = "OTEL_SERVICE_NAME"

// validateIdentity refuses a value the rest of the chain would choke on.
//
// Every one of these reaches a URL path segment and the generated Collector
// configuration, so they are checked once here rather than at each use.
func validateIdentity(identity devIdentity) error {
	for _, field := range []struct{ name, value string }{
		{"project", identity.Project},
		{"agent", identity.Agent},
		{"candidate", identity.Candidate},
		{"environment", identity.Environment},
		{"run", identity.Run},
		{"behavioral profile", identity.Profile},
	} {
		if err := validateCollectorScalar(field.name, field.value); err != nil {
			return err
		}
		// A path separator would change the shape of a /v1 route rather than
		// name a resource. The client escapes segments, so this is belt and
		// braces — and the message is clearer than a 404 from the server.
		if strings.ContainsAny(field.value, "/\\") {
			return fmt.Errorf("%s %q contains a path separator", field.name, field.value)
		}
	}
	return nil
}

// sanitizeRunSegment makes a candidate id usable inside a generated run id.
//
// Only the generated run id goes through this. A candidate the caller supplied
// is never rewritten — see validateIdentity, which refuses rather than repairs.
func sanitizeRunSegment(candidate string) string {
	replaced := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '.':
			return r
		default:
			return '-'
		}
	}, candidate)
	return strings.Trim(replaced, "-")
}

// ---------------------------------------------------------------------
// git
// ---------------------------------------------------------------------

// repositoryInfo is what dev could learn about the working directory.
type repositoryInfo struct {
	isRepository bool
	root         string
	shortSHA     string
	dirty        bool
}

// projectName is the repository's directory name, else the working directory's.
func (r repositoryInfo) projectName(workloadDir string) string {
	if r.isRepository && r.root != "" {
		return filepath.Base(r.root)
	}
	return filepath.Base(workloadDir)
}

// gitTimeout bounds each git invocation.
//
// A repository on a slow or unreachable network filesystem must not hang dev
// before it has printed anything. Every query here is local and answers in
// milliseconds; this only bounds the pathological case.
const gitTimeout = 5 * time.Second

// inspectRepository asks git about the working directory.
//
// git is a subprocess, not a dependency: nothing is imported, and its absence is
// a condition to report rather than a build constraint. The root module takes on
// no new third-party dependency for this.
//
// Every failure is silent and leaves the field unset. That is deliberate — the
// caller decides what a missing commit means, and it decides differently for a
// project name (fall back to the directory) than for a candidate (refuse).
func inspectRepository(workloadDir string) repositoryInfo {
	info := repositoryInfo{}

	root, err := runGit(workloadDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return info
	}
	info.isRepository = true
	info.root = root

	// --short rather than the full hash: a candidate identifier is read by
	// people, and the full hash adds 33 characters of nothing.
	if sha, err := runGit(workloadDir, "rev-parse", "--short", "HEAD"); err == nil {
		info.shortSHA = sha
	}

	// --porcelain is the stable, parseable form; any output at all means the
	// worktree differs from HEAD. Untracked files count: a new file the agent
	// imports changes its behavior as much as an edited one.
	if status, err := runGit(workloadDir, "status", "--porcelain"); err == nil {
		info.dirty = status != ""
	}
	return info
}

// runGit runs one git query in the workload's directory.
func runGit(workloadDir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	// -C rather than cmd.Dir, so the command line says which repository it asked
	// about and a failure is readable in a diagnostic.
	full := append([]string{"-C", workloadDir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// git reads configuration from the environment; inheriting it is correct.
	// Nothing here passes a caller-supplied value as an argument: every element
	// of args is a literal in this file.
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
