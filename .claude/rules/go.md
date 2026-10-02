# Go Conventions

These are the conventions this repository actually follows, not generic
Go advice. When in doubt, match the existing packages (`event`,
`internal/features` … `internal/otel`) rather than introducing a new
style.

## Toolchain

- Go 1.27 (see `go.mod`). Use current-version idioms: `range N` instead
  of a classic counting loop, `b.Loop()` instead of `for i := 0; i <
  b.N; i++` in benchmarks, `min`/`max` builtins instead of manual
  clamping, `maps.Copy` instead of a manual copy loop, `omitzero`
  (not `omitempty`, which does nothing on struct fields) for JSON
  struct tags.
- `gofmt -l` must report nothing before any work is considered done.
  `go vet ./...` must be clean. `go test ./...` must pass — with
  `-race` for anything touching `internal/store` or `internal/baseline`.

## Package shape

- The root `trustvian` package and the `event` package are the *only*
  two packages an external module can import — everything else lives
  under `internal/`, which Go's compiler enforces. See
  `.claude/rules/architecture.md` for why `event` specifically had to
  move out of `internal/` in Slice 8.
- No `pkg/` directory. `internal/` is the encapsulation mechanism; a
  parallel `pkg/` layer would just be Java/.NET ceremony CLAUDE.md
  explicitly says to avoid.
- One package, one responsibility, following the pipeline stages:
  `event` → `internal/features` → `internal/fingerprint` →
  `internal/baseline`/`internal/store` → `internal/anomaly` →
  `internal/trust` → `internal/policy` → root `Engine`. A new file
  belongs in the package matching the stage it computes, not wherever
  is convenient.

## Types and construction

- Domain values (`Event`, `Actor`, `Features`, `Fingerprint`,
  `Anomaly`, `Trust`, `Policy.Result`) are treated as immutable: no
  method mutates its receiver. Where a value needs to evolve
  (`Baseline.Observe`), the method returns a *new* value
  (copy-on-write) rather than mutating in place — this is what makes a
  `Store.Get` result safe to read without holding a lock afterward.
- Prefer a plain struct literal over a constructor. Add a `New` only
  when there's real work to do at construction (`baseline.New` seeds
  an empty map; `store.NewInMemory` allocates the shard map). Don't
  add one "for symmetry."
- Functional options (`Option func(*Engine)`) are the configuration
  pattern for `Engine`, matching `NewEngine(opts ...Option)`. Don't
  introduce a second configuration mechanism (builder struct, config
  file loader) without a concrete need driving it.
- Sentinel errors (`var ErrX = errors.New(...)`), wrapped with
  `fmt.Errorf("...: %w", err)`, checked with `errors.Is`. No custom
  error types unless a caller genuinely needs to extract structured
  data from the error.

## What to avoid

- No reflection. No code generation. Three third-party dependencies, and
  each is **confined to exactly one package**:

  | Dependency | Only importable from | Why the confinement |
  |---|---|---|
  | `go.opentelemetry.io/otel{,/sdk,/trace}` | `internal/otel` | Keeps the core engine OTel-independent (a CLAUDE.md requirement, not a preference) |
  | `go.yaml.in/yaml/v3` | `config` (and the test-only `scripts` package, which parses workflow YAML) | Config parsing is an adapter; no domain package knows YAML exists |
  | `github.com/jackc/pgx/v5` | `internal/store/postgres` | Keeps the driver out of the build of `internal/store` and every core package (`v0.8` task 035) |

  The confinement is the rule, not the count. Adding a fourth means
  naming the single package that may import it and saying why nothing
  else may — and verifying it with `go list -deps`, which today reports
  zero pgx and zero OTel packages in the dependency graph of `event`,
  `internal/features` through `internal/policy`, `internal/store`, and
  the root `trustvian` package. If a dependency cannot be contained that
  way, that is an argument against adding it.
- No package-level mutable state. `Engine` is always constructed
  explicitly and passed around; there is no default global engine for
  ergonomics, per Architecture Risk #8 in the project's design notes.
- Don't add a config knob "for future flexibility" without a current
  caller. `internal/anomaly.Config` and `internal/trust.Config` exist
  because tests and the CLI need to construct deterministic scenarios
  — that's the bar for a new one.
