# 0055 — A scenario suite is a bounded schedule and a report, not an evaluation

**Status:** Accepted

## Context

Task 078 asks for suites of behavioral scenarios: one invocation, many
scenarios, a machine-readable summary, and "one failure does not abort the rest
unless asked". Earlier slices already gave each scenario its own evaluation:

- [ADR 0053](0053-repeated-evaluation-counts-identities-across-isolated-repetitions.md)
  added the repeated comparison and its six checks.
- [ADR 0054](0054-scenario-executions-are-persisted-and-references-resolved-by-the-control-plane.md)
  added persisted executions and recorded references.

What remained was to decide what a suite adds without becoming a second gate.
A suite also needs real deadlines. A scenario that hangs inside one invocation
must not hold up all the others, and the existing per-request HTTP timeout
bounds no workload.

## Decisions

### 1. A suite is a directory, run in name order

```text
trustvian eval run --suite DIRECTORY --scenario-timeout DURATION
                   [--fail-fast] [--reference last] [--json] …
```

**Discovery:**
- Members are the directory's immediate regular `.yaml` and `.yml` files, in
  byte order of their names. There is no recursion and no manifest; a directory
  is assumed, as task 078's open question 3 proposed.
- A symbolic link is refused rather than followed, because a suite's members are
  the files in that directory.
- `--suite` and `--scenario` are mutually exclusive.

**Bounds:**
- At most 64 scenario files.
- At most 4096 directory entries are examined, read 256 at a time, so an
  arbitrarily large directory is refused rather than read into memory.
- Each file is already bounded by the config loader.

**Preflight, all before any workload or request:**
- every file is loaded and validated;
- duplicate scenario names are refused, because a name is what `last` matches;
- every member's scope is derived.

Any failure exits `2`.

### 2. Members run sequentially, each exactly as a single scenario

Each member runs through the same code path as `--scenario`. It keeps its own N,
its own explicit limits, fresh run ids and profiles, its own persisted execution
and the control plane's verdict.

**The suite computes nothing behavioral.** It pools no evidence across
scenarios, combines no presence counts and recomputes no verdict. A member's
outcome is that member's exit code:

| Exit | Outcome |
|---|---|
| `0` | `pass` |
| `1` | `fail` |
| `2` or `3` | `error` |

The suite introduces no database entity and no schema step.

**Scheduling after a failure:**
- By default the suite continues after any member's FAIL or error.
- `--fail-fast` stops scheduling after the first member that is not a PASS.
- Every member not run is reported `skipped`, with a reason: `fail_fast` or
  `cancelled`.
- A skipped member has no exit code and is never a pass.

### 3. `--reference last`, resolved per member; explicit ids are refused

With a suite, `--reference` accepts only `last`. It is resolved independently
for each member, by the existing begin call, from that member's own name,
project, agent, environment and N. Latest-first selection, exact-N validation
and the no-fallback rule therefore hold unchanged.

A missing or unusable reference is that member's operational error, raised
before it launches any workload; the other members still run.

One explicit execution id cannot be every scenario's reference, so it is a usage
error with `--suite`. It remains available with `--scenario`. A per-member
reference map is deferred.

### 4. Every member has a real deadline

`--scenario-timeout` is required with `--suite`, from 1s to 24h. It bounds the
member's whole scenario: begin, every repetition including `trustvian dev`'s
composition and teardown, and completion.

When the deadline passes:

- **No further repetition of that scenario starts.**
- **The running workload is stopped.**
  1. Its process group gets SIGTERM.
  2. After a 5s grace, SIGKILL.
  3. Once the leader is reaped, whatever is left of the group is killed.

  A descendant that ignores SIGTERM or outlives its parent therefore does not
  survive the scenario.
- **The run is failed, never completed,** whatever the workload exits with. A
  workload that traps SIGTERM and exits 0 has not succeeded.
- **The execution is recorded failed,** best effort, under its own 10s cleanup
  context rather than the expired one. A failed execution is never a
  reference.
- **The member is an operational error** with code `scenario_timeout`.

The pending SIGKILL is cancelled as soon as the leader is reaped. A stale
timer can therefore never signal a process-group id the system has since
reused, for example for the next member's processes.

`eval run` refuses on Windows, as `trustvian dev` does. Every repetition is a
`dev` session, and a platform that cannot stop what it starts cannot honour a
deadline.

The deadline reaches `trustvian dev` as an optional field of its in-process
configuration. Neither `dev`'s command line nor single-scenario mode sets it,
so their signal, terminal, stdin and exit-status behavior is unchanged.

**SIGINT or SIGTERM to a suite:**
- The running member is stopped, through the same relay that forwards the
  signal to its workload, and recorded failed with code `cancelled`.
- Every member not yet scheduled is `skipped` with reason `cancelled`.
- The suite exits `3`.

### 5. One bounded, versioned document

`--json` writes exactly one suite document:

- `version`, `complete`;
- the suite (`directory`, `scenario_count`);
- the options (`scenario_timeout`, `fail_fast`, `reference`);
- one entry per member, carrying:
  - `file` and `scenario` (`name`, `runs`);
  - `outcome` and `exit_code`;
  - `execution_id`;
  - for a PASS or FAIL, its single-scenario result document **embedded
    unchanged** as `result`;
  - for an error, a bounded `error` (`code` and a message of at most 1024
    bytes);
  - for a skip, `skipped_reason`;
- a summary, `exit_code`, and the CLI producer version.

Workload output and progress go to stderr, as in single-scenario mode.

**The suite's exit code is the most severe of its members': `3`, then `2`,
then `1`, then `0`.** This keeps every existing meaning. A suite whose
preflight fails exits `2` before anything runs.

**The encoded document is capped at 32 MiB.**
- Member results stop being retained once they would exceed the cap, so memory
  is bounded too.
- On overflow the suite exits `3` and writes an *incomplete* document instead:
  `complete: false`, an `output_too_large` error, and each member's file and
  scenario identity.
- No outcome, exit code or result appears in it, so no PASS can be read from
  evidence that was not delivered.
- Comparison evidence is never truncated to fit.

## Alternatives considered

**Pool the members into one comparison or one verdict.** Rejected. Each
scenario's N, k, j and limits are its author's. A combined count would be a
fourth evaluation rule that nobody wrote, and it would diverge from the members'
own verdicts.

**Run members in parallel.** Rejected for this slice, for the reason
repetitions are sequential (task 078, open question 5): shared services would
measure contention.

**A suite-level timeout instead of a per-scenario one.** Rejected. It cannot
tell which scenario hung, and it lets one scenario consume every other
scenario's time.

**Truncate oversized results.** Rejected. A partial comparison read as a whole
one is the failure the incomplete-evidence rules exist to prevent.

**A suite manifest file, or recursion.** Deferred, with reference maps and
other configuration formats.

## Consequences

- **New CLI surface:** `--suite`, `--scenario-timeout` and `--fail-fast` on
  `trustvian eval run`, and the suite result document. No `/v1` or schema
  change.
- **`trustvian dev`'s in-process configuration** gains an optional deadline that
  only suites set.
- **Not built:**
  - the GitHub Action (079);
  - an execution listing;
  - repeated counted-change limits;
  - scenario and input versioning;
  - per-member reference maps;
  - parallel members.
