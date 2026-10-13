# frequency-abuse

The one scenario in this directory with no corresponding
[docs/use-cases.md](../../docs/use-cases.md) section to port — the
`frequency_deviation` anomaly signal
([docs/archive/tasks/v0.1/004-anomaly.md](../../docs/archive/tasks/v0.1/004-anomaly.md)) did not
exist when that document was written.

A service (`svc-poller`) polls the same status endpoint
(`StatusService.GetStatus` on `status-service`) on a consistent
10-second cadence, 25 times. This matures the baseline's `IntervalMean`
(an EWMA of inter-observation intervals — see
[`internal/baseline.FingerprintStats`](../../internal/baseline/baseline.go)).
All `Timestamp`s are constructed explicitly (`base.Add((i-1) *
10*time.Second)`), not produced by real `time.Sleep` calls, so the
example runs instantly and deterministically. One final event arrives
only 50ms after the previous one — a burst far outside the learned
cadence — and `frequency_deviation` appears in the result's
`Anomaly.Contributors`, even though the fingerprint itself is now fully
familiar (`categorical_novelty` has decayed to zero and drops out of
`Contributors` entirely, since only non-zero-value signals are
reported).

The program asserts `frequency_deviation` is actually present before
exiting (`log.Fatal` otherwise), so a regression in
`internal/anomaly.Score`'s frequency signal would fail this example, not
just silently produce different-looking output.

## The signal is opt-in

As of the v0.1 hardening pass, `anomaly.DefaultConfig()` sets
`FrequencyWeight` to `0`. The signal is still computed and still
reported in `Anomaly.Contributors` at full strength — this example's
output below shows `frequency_deviation: 1.00` — but it contributes
nothing to `Anomaly.Score` until an operator opts in, exactly the way
`SensitiveTargetFloor` ships empty and inert.

The reason is calibration. The signal is a z-score of the current
interval against an EWMA whose standard deviation is whatever jitter
that fingerprint's traffic happens to carry. On a service with only a
few milliseconds of natural jitter around a ten-second cadence, an
entirely ordinary event a few milliseconds off the mean already exceeds
`FrequencyZThreshold` and clamps to `1.00`; at the previous `0.6`
default that alone carried a fully-familiar actor to `RiskHigh` on
routine traffic. There is no one weight that is right for a cron job, a
chatty RPC poller, and a human-driven UI at once, so the project ships
the mechanism rather than a guess.

Enabling it is one `Option`, compiled through the public `config`
package (see [docs/anomaly-config-guide.md](../../docs/anomaly-config-guide.md)):

```go
ac, err := config.CompileAnomaly(config.AnomalyConfig{
	Version:         config.AnomalySchemaVersionV1,
	FrequencyWeight: 0.6, // after measuring your own fleet's jitter
})
if err != nil {
	log.Fatal(err)
}
engine := trustvian.NewEngine(trustvian.WithAnomalyConfig(ac))
```

This program keeps the default on purpose. It prints the signal's
`Value` and `Weight` side by side, so the distinction between "detected"
and "counted" is visible in the output rather than only in prose.

Run it:

```bash
cd examples/frequency-abuse && go run .
```

## Real output

```
poll  1: confidence=0.00 trust=0.95 decision=observe_only learned=true
poll 10: confidence=0.45 trust=0.71 decision=observe_only learned=true
poll 20: confidence=0.95 trust=0.90 decision=observe_only learned=true
poll 25: confidence=1.00 trust=0.95 decision=observe_only learned=true

Decision: observe_only
trust 0.95 (low): identity confidence 0.95, anomaly 0.00 at full confidence, context risk 0.00
Anomaly score: 0.00 (confidence 1.00)
Detected:
  - frequency_deviation: 1.00 (interval 50ms deviates from a stable baseline of 10s (stddev ~0))
Policy: default action (no policy rules configured; observing by default)

frequency_deviation: value=1.00 weight=0.00 -> contributes 0.00 to Anomaly.Score
Signal present in Contributors, as expected. Raise FrequencyWeight to make it count.
```

Unlike the final events of `basic`, `credential-misuse`,
`unexpected-dependency`, `external-destination` and `ai-agent`, this one's
`Anomaly.Confidence`
is `1.00` (the fingerprint is fully mature) and `categorical_novelty`
does not appear in `Contributors` at all — the sole entry under
`Detected:` is `frequency_deviation`, at its maximum value of `1.00`.
This is what distinguishes "abnormal request frequency" from plain
cold-start novelty: a familiar actor doing a familiar thing, just far
too often.

`Anomaly.Score` is nonetheless `0.00`, and `Trust` stays at `0.95`
(`low`), because the default `FrequencyWeight` of `0` makes this
signal's contribution to `internal/anomaly.combine`'s noisy-OR
`1.00 × 0 = 0` — see [the section above](#the-signal-is-opt-in). Set
`FrequencyWeight` to `0.6` and the same run reports
`Anomaly score: 0.60` and `trust 0.38 (high)` instead. See
[the examples index](../README.md#a-note-on-decision) for why `Decision`
here is `observe_only` rather than a differentiated `ALERT`/`BLOCK`.
