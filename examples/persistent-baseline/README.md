# persistent-baseline

Demonstrates [task 034](../../docs/archive/tasks/v0.8/034-production-store-contract-and-public-boundary.md)'s
public storage boundary: selecting a **durable** Store through
`config.StorageConfig` + `config.CompileStorage`, so a learned baseline
survives a process restart.

**This example could not be written by an external consumer before task
034.** `store.Store` lives in `internal/store`, and its methods
reference internal types (`baseline.Baseline`, `fingerprint.Fingerprint`,
`features.VolatileFeatures`), so an outside caller could neither
implement the interface nor call `store.NewFileStore` — and no exported
function returned one for pass-through. Every external deployment was
therefore silently pinned to the default in-memory store, losing all
learned behavior on restart. See
[ADR 0018](../../docs/adr/0018-production-store-boundary-and-postgresql-direction.md).

The restart here is real in the sense that matters: the first `Engine` is
discarded and a second is built from scratch against the same state
file, reading it back off disk.

Run it:

```bash
cd examples/persistent-baseline && go run .
```

## Real output

```
process 1, event  1: confidence=0.00 trust=0.95
process 1, event 12: confidence=0.55 trust=0.71

state file written: 1009 bytes

process 2 (fresh Engine, same state file):
Decision: observe_only
trust 0.72 (medium): identity confidence 0.95, anomaly 0.40 at 60% confidence, context risk 0.00
Anomaly score: 0.40 (confidence 0.60)
Detected:
  - categorical_novelty: 0.40 (fingerprint observed 12/20 times required for maturity)
Policy: default action (no policy rules configured; observing by default)


Baseline survived the restart — confidence 0.60, not 0.00.
```

The load-bearing line is `fingerprint observed 12/20 times required for
maturity`. A fresh `Engine` with no in-memory history could only report
*never observed* — the 12 it reports are the observations the first
process wrote to disk.

## The configuration

```go
cfg := config.StorageConfig{
	Version: config.StorageSchemaVersionV1,
	Type:    config.StorageTypeFile,
	File:    &config.FileStorageConfig{Path: path},
}

s, err := config.CompileStorage(cfg)   // opens the store, reads existing state
if err != nil {
	log.Fatal(err)                      // never falls back to a non-durable store
}
engine := trustvian.NewEngine(trustvian.WithStore(s))
```

or, equivalently, from YAML (`config.LoadStorageFile`, or the CLI's
`trustvian analyze --storage-config` / `baseline build --storage-config`):

```yaml
version: v1
type: file
file:
  path: /var/lib/trustvian/baseline.json
```

`s`'s static type is `store.Store` — the same internal interface
`internal/store` itself uses — but this code never names that type or
imports `internal/store`: `s` is received from `CompileStorage` and
passed straight into `trustvian.WithStore` via Go's normal type
inference, the identical mechanism `config.CompilePolicy` and
`config.CompileAnomaly` already rely on
([ADR 0008](../../docs/adr/0008-policy-config-boundary.md)).

## Fail-closed, not fail-quiet

`CompileStorage` returns a nil Store on *any* error, so a misconfigured
or unopenable store aborts startup rather than degrading to memory. That
matters more here than for Policy or anomaly config: silently
substituting a non-durable store for an explicitly requested durable one
would lose exactly the state the operator asked to keep. `type: postgres`
is fully implemented as of `v0.8`, and the same rule applies to it: a
database that is unreachable, unauthenticated, or missing its DSN yields an
error and no Store — never a substitute. See
[`main_test.go`](main_test.go)'s own
`TestPublicConfigPostgresFailsClosedWhenUnreachable`,
`TestPublicConfigPostgresMissingDSNFailsClosed`,
`TestPublicConfigSelectsPostgresStoreAcrossRestart` (skipped unless
`TRUSTVIAN_TEST_POSTGRES_DSN` is set),
and `TestPublicConfigMemoryStoreDoesNotPersist` (the control proving the
durable case is genuinely doing something).

## What this example does not show

PostgreSQL. The program itself uses `type: file`, so it runs with no
database. The PostgreSQL store (`type: postgres`) is covered by the
`main_test.go` tests above, and the contract every store implements is
executable: `TestStoreContract` in
[`internal/store/contract_test.go`](../../internal/store/contract_test.go)
runs the same assertions against the in-memory and file stores, and
against PostgreSQL when `TRUSTVIAN_TEST_POSTGRES_DSN` is set.
