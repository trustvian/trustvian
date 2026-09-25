# Security Model

This document describes the threats Trustvian's design accounts for,
what's actually implemented today versus deliberately deferred, and
where to find each protection in code. See
[`.claude/rules/security.md`](../.claude/rules/security.md) for the
same principles phrased as engineering rules for future changes.

Trustvian is not itself an authentication system, a database, or an
enforcement point in the network path — it is a scoring and decision
engine. Its security model is about the integrity of *its own
reasoning* (can its scores/decisions be manipulated or bypassed), not
about securing the transport or storage layers around it (those are
explicitly out of core scope — see
[`docs/ARCHITECTURE.md`](ARCHITECTURE.md)).

**Reporting a vulnerability:** privately, never in a public issue — see
[`.github/SECURITY.md`](../.github/SECURITY.md).

## Test index

Every threat below is backed by a specific, named test, not just a
design argument. This table exists so that fact is verifiable at a
glance rather than requiring a read of every section
([`docs/archive/tasks/v0.1/012-security-tests.md`](archive/tasks/v0.1/012-security-tests.md)).
Threats whose tests already existed before that task are referenced
here, not moved or rewritten.

| Threat | Test(s) |
| --- | --- |
| Shared PostgreSQL backend keeps credentials and internals off every surface | `TestPostgresErrorsNeverCarryTheDSN`, `TestPostgresIdentifierOrderingIsByteOrder`, `TestPostgresBuildsNoSQLFromNonConstants`, `TestPostgresRunScopedWritesTakeTheRowLock`, `TestPostgresUsesReadCommitted`, `TestPostgresRefusesANewerSchema`, `TestPostgresRefusesTablesWithoutAVersionRow`, `TestPostgresConcurrentInitializationYieldsOneSchema`, `TestPostgresMigrationDoesNotBlockOnTheEngineLock` in [`platform/postgres_internal_test.go`](../platform/postgres_internal_test.go), [`postgres_concurrency_test.go`](../platform/postgres_concurrency_test.go) and [`postgres_schema_test.go`](../platform/postgres_schema_test.go); `TestRuntimeErrorsNeverCarryTheDSN`, `TestUnknownBackendFailsClosed`, `TestPostgresBackendWithoutDSNFailsBeforeTheListener`, `TestDiscoveryNeverCarriesBackendConfiguration`, `TestRuntimeOnPostgresServesTheRealAPI`, `TestBackendSelectionDoesNotLeakAboveComposition` in [`platform/localruntime/backend_test.go`](../platform/localruntime/backend_test.go) and [`architecture_test.go`](../platform/localruntime/architecture_test.go) |
| Recorded promotion decisions cannot be forged, rewritten, or re-derived | `TestRestorePreservesAHistoricallyInconsistentCheck`, `TestRestoreDoesNotRecomputeTheVerdictFromTheFlags`, `TestGateRestoreEvaluatesNothing`, `TestPromotionDeclaresNoDeploymentOrApprovalField`, `TestPromotionTypesAreTheOnesTask066Specified` in [`platform/promotion_test.go`](../platform/promotion_test.go), [`promotion_internal_test.go`](../platform/promotion_internal_test.go) and [`gate_internal_test.go`](../platform/gate_internal_test.go); `TestPostgresPromotionCommitWindowIsMutuallyExclusive`, `TestPostgresPromotionWaitsForTheLockAndSeesTheNewState`, `TestPostgresPromotionLocksEnvironmentsInByteOrder`, `TestPostgresPromotionLocksTwoEnvironmentRowsAndNothingElse` in [`platform/postgres_concurrency_test.go`](../platform/postgres_concurrency_test.go); `TestPostgresMigratesV3ToV4AddingAnEmptyHistory` in [`platform/postgres_schema_test.go`](../platform/postgres_schema_test.go), `TestSchemaV3MigratesToV4PreservingContent` in [`platform/promotion_migration_test.go`](../platform/promotion_migration_test.go); `TestPromotionUIDecidesNothing`, `TestPromotionUIClaimsNoDeployment` in [`platform/webui/assets_test.go`](../platform/webui/assets_test.go) |
| Browser control plane renders untrusted text inertly | `TestShippedScriptsUseNoDangerousRenderingPrimitive`, `TestShippedScriptsDoNotEnumerateServerObjects`, `TestFieldAllowlistsExcludeEverySensitiveField`, `TestPrivacyFixtureFieldsAreUnreachable`, `TestContentSecurityPolicyForbidsEveryEscapeHatch`, `TestNoBrowserPersistenceOfPlatformState`, `TestUint64CountersAreNeverParsedAsNumbers`, `TestGateVerdictComesFromTheServer` in [`platform/webui/assets_test.go`](../platform/webui/assets_test.go) and [`handler_test.go`](../platform/webui/handler_test.go); `TestHandlerConstructorTakesNoControlPlane`, `TestDependencyGraphContainsNoPlatformOrStoreCode` in [`platform/webui/architecture_test.go`](../platform/webui/architecture_test.go); `TestUnknownAPIRoutesNeverReturnTheShell`, `TestWebUIResponsesCarryNoCORSHeader` in [`platform/localruntime/runtime_test.go`](../platform/localruntime/runtime_test.go); `TestHostileServerStringsSurviveTheAPIAsData` in [`platform/localruntime/endtoend_test.go`](../platform/localruntime/endtoend_test.go) |
| Identity confusion (cross-actor isolation) | `TestAnalyzeCrossActorIsolation` in [`engine_test.go`](../engine_test.go) |
| Baseline poisoning | `TestObserveLearnsOnlyFromEligibleDecisions`, `TestAnalyzeSensitiveTargetFloorEndToEnd` in [`engine_test.go`](../engine_test.go); `TestFingerprintStatsIgnoresNonPositiveInterval`, `TestFingerprintStatsOutOfOrderObservationDoesNotDistortNextInterval` in [`internal/baseline/baseline_test.go`](../internal/baseline/baseline_test.go); `TestScoreFrequencyDeviation` (negative-interval subtests) in [`internal/anomaly/anomaly_test.go`](../internal/anomaly/anomaly_test.go) |
| Malicious agents / privilege escalation | `TestAnalyzeSensitiveTargetFloorEndToEnd` in [`engine_test.go`](../engine_test.go) |
| Policy bypass | `TestEvaluateFailsClosedOnZeroValuePolicy`, `TestEvaluateFailsClosedOnInvalidDefaultAction`, `TestEvaluateFailsClosedOnEmptyDefaultReason` in [`internal/policy/policy_test.go`](../internal/policy/policy_test.go) |
| Malformed events / extreme input values | `TestValidateRejectsNonFiniteIdentityConfidence`, `TestValidateAcceptsVeryLongActorID` in [`event/event_test.go`](../event/event_test.go); `TestAnalyzeNegativeDurationDoesNotCorruptTrustScore`, `TestAnalyzeLargeAttributesMapDoesNotPanic` in [`engine_test.go`](../engine_test.go) |
| Concurrency issues | `TestInMemoryObserveConcurrentSameKey`, `TestInMemoryObserveConcurrentDistinctKeys` in [`internal/store/store_test.go`](../internal/store/store_test.go); `TestFileStoreObserveConcurrentSameKey`, `TestFileStoreObserveConcurrentDistinctKeys` in [`internal/store/file_test.go`](../internal/store/file_test.go) |
| Resource exhaustion | `TestAnalyzeLargeAttributesMapDoesNotPanic`, `TestFingerprintFloodStaysBoundedEndToEnd` in [`engine_test.go`](../engine_test.go); `TestAdversarialStreamStaysBounded`, `TestRejectsOneBeyondCapacity` in [`internal/baseline/admission_test.go`](../internal/baseline/admission_test.go) |
| Explainability | `TestEvaluateAlwaysProducesNonEmptyExplanationReason` in [`internal/policy/policy_test.go`](../internal/policy/policy_test.go) |
| Alert/notification delivery integrity | `TestSendSignsPayloadCorrectly`, `TestSendTamperedPayloadFailsVerification`, `TestSendDoesNotLeakSecret`, `TestNewWebhookSinkRejectsNonHTTPS`, `TestNewWebhookSinkRejectsLoopbackDestination`, `TestSendRespectsTimeout`, `TestSendPayloadTooLargeMakesNoNetworkCall` in [`alert/webhook_test.go`](../alert/webhook_test.go) |
| Configuration-input validation | `TestValidateRejectsUnsupportedVersion`, `TestValidateRejectsInvalidDefaultDecision`, `TestValidateRejectsInvalidRuleDecision`, `TestValidateRejectsInvalidActorType`, `TestValidateRejectsInvalidOperationCategory`, `TestValidateRejectsInvalidRiskLevel`, `TestValidateRejectsDuplicateRuleName`, `TestValidateRejectsEmptyRuleName`, `TestValidateRejectsTooManyRules`, `TestValidateRejectsOverlongName` in [`config/validate_test.go`](../config/validate_test.go); `TestLoadRejectsUnknownTopLevelField`, `TestLoadRejectsUnknownNestedField`, `TestLoadRejectsDuplicateYAMLKeys`, `TestLoadFileRejectsOversizedFile`, `TestLoadRejectsEmptyInput`, `TestLoadDoesNotPanicOnArbitraryInput`, `FuzzLoad` in [`config/load_test.go`](../config/load_test.go)/[`config/fuzz_test.go`](../config/fuzz_test.go) |
| Anomaly configuration-input validation — weight/threshold ranges, negative-into-`uint64` rejection, backward-compatible zero-value default (`v0.7` task 033) | `TestValidateAnomalyConfigRejectsInvalidWeight`, `TestValidateAnomalyConfigRejectsInvalidPointerWeight`, `TestValidateAnomalyConfigRejectsInvalidZThreshold`, `TestValidateAnomalyConfigAcceptsZeroMinObservations`, `TestValidateAnomalyConfigRejectsInvalidSensitiveTargetFloor` in [`config/anomaly_test.go`](../config/anomaly_test.go); `TestCompileAnomalyZeroValueMatchesDefaultConfig`, `TestCompileAnomalyTranslatesEveryField` in [`config/anomaly_compile_test.go`](../config/anomaly_compile_test.go); `TestLoadAnomalyRejectsNegativeIntoUnsignedField`, `TestLoadAnomalyRejectsUnknownField`, `FuzzLoadAnomaly` in [`config/anomaly_load_test.go`](../config/anomaly_load_test.go) |
| Storage configuration & persistence contract — fail-closed on unbuildable store, no implicit backend, no lost updates under concurrency, load-failure propagation (`v0.8` task 034) | `TestStoreContract` (incl. its same-key-concurrency guarantee), `TestStoreContractImplementationsAgreeOnLogicalState` in [`internal/store/contract_test.go`](../internal/store/contract_test.go); `TestCompileStoragePostgresFailsClosedNeverFallsBack`, `TestCompileStorageInvalidConfigReturnsNilStore`, `TestCompileStorageFilePropagatesLoadFailure`, `TestValidateStorageConfigRejectsMissingType`, `FuzzLoadStorage` in [`config/storage_test.go`](../config/storage_test.go); `TestRunAnalyzeUnimplementedStorageBackendFailsClosed`, `TestRunAnalyzeInvalidStorageConfigFailsClosed`, `TestBaselineBuildThenAnalyzePersistsAcrossCommands` in [`cmd/trustvian/main_test.go`](../cmd/trustvian/main_test.go) |
| Alert configuration-input validation | `TestValidateAlertConfigRejectsUnsupportedVersion`, `TestValidateAlertConfigRejectsInvalidSeverity`, `TestValidateAlertConfigRejectsInvalidDecision`, `TestValidateAlertConfigRejectsInvalidRiskLevel`, `TestValidateAlertConfigRejectsInvalidActorType`, `TestValidateAlertConfigRejectsInvalidTargetCategory`, `TestValidateAlertConfigRejectsInvalidMinAnomalyScore`, `TestValidateAlertConfigRejectsInvalidMaxTrustScore`, `TestValidateAlertConfigRejectsDuplicateRuleName`, `TestValidateAlertConfigRejectsEmptyRuleName`, `TestValidateAlertConfigRejectsTooManyRules` in [`config/alert_test.go`](../config/alert_test.go); `TestLoadAlertsRejectsUnknownField`, `TestLoadAlertsRejectsDuplicateYAMLKeys`, `TestLoadAlertsFileRejectsOversizedFile`, `TestLoadAlertsRejectsEmptyInput`, `FuzzLoadAlerts` in [`config/alert_load_test.go`](../config/alert_load_test.go) |
| Runtime health endpoints — no information leak, liveness independent of the store, bounded probes, no fallback (`v0.9` task 042) | `TestHandlerLeaksNothing`, `TestFailingProbeDoesNotAffectLiveness`, `TestReadyIsBoundedByProbeTimeout`, `TestDrainingIsNeitherLiveNorReady` in [`processor/internal/health/`](../processor/internal/health/); `TestNoHealthConfigServesNothing`, `TestShutdownTransitionsReadinessBeforeClosingStore`, `TestDoubleShutdownIsSafe` in [`processor/lifecycle_test.go`](../processor/lifecycle_test.go); `TestPingReportsDatabaseUsability` in [`internal/store/postgres/hardening_test.go`](../internal/store/postgres/hardening_test.go) |
| Operational metrics privacy and cardinality (`v0.9` task 043) | `TestNoForbiddenAttributes`, `TestUnknownDecisionIsFolded` in [`processor/internal/metrics/metrics_test.go`](../processor/internal/metrics/metrics_test.go); `TestObserveErrorRecordsBoundedCategory`, `TestMetricsUnderConcurrentSpans` in [`processor/metrics_test.go`](../processor/metrics_test.go) |
| Backup and restore — no credential exposure, corrupt-backup rejection, never overwriting a live database, failed restores quarantined, behavioral recovery, upgrade without state reinterpretation (`v0.9` task 044) | `TestBackupScriptRefusesUnsafeInvocations`, `TestRestoreScriptRefusesUnverifiableBackups`, `TestBackupRestorePreservesLearnedBehavior`, `TestBackupDuringConcurrentWritesIsConsistent`, `TestBackupRestoreFailClosed`, `TestUpgradeFromPreviousReleasePreservesLearnedState` in [`scripts/backup_restore_test.go`](../scripts/backup_restore_test.go) |
| Sequence state (memory bounds, ordering, cross-actor isolation) | `TestBaselineObservePredecessorCountsIsBounded`, `TestBaselineObserveOutOfOrderEventDoesNotRecordOrCorruptTransition`, `TestBaselineObservePredecessorCountsIsImmutable` in [`internal/baseline/baseline_test.go`](../internal/baseline/baseline_test.go); `TestInMemoryObserveConcurrentTransitionTracking` in [`internal/store/store_test.go`](../internal/store/store_test.go); `TestDefaultConfigTransitionWeightIsOptIn`, `TestScoreTransitionDeviation` in [`internal/anomaly/anomaly_test.go`](../internal/anomaly/anomaly_test.go); `TestAnalyzeTransitionDeviationEndToEnd` in [`engine_test.go`](../engine_test.go) |
| Transition rarity — cold start, counter overflow, poisoning, actor isolation (`v0.6` task 026) | `TestBaselineObserveManyDistinctTransitionsStayBounded`, `TestBaselineObserveOutgoingTransitionTotalIsImmutable` in [`internal/baseline/baseline_test.go`](../internal/baseline/baseline_test.go); `TestScoreTransitionRarityColdStart`, `TestScoreTransitionRarityNeverExceedsBounds`, `TestDefaultConfigTransitionRarityWeightIsOptIn` in [`internal/anomaly/anomaly_test.go`](../internal/anomaly/anomaly_test.go); `TestAnalyzeTransitionRarityCrossActorIsolation`, `TestAnalyzeTransitionRarityScoresBeforeLearning`, `TestObserveTransitionRarityLearnsOnlyFromEligibleDecisions` in [`engine_test.go`](../engine_test.go) |
| Bounded 3-gram detection — independent cardinality bounds, counter overflow, poisoning, actor isolation (`v0.6` task 027) | `TestBaselineObserveTrigramCountsIsBounded`, `TestBaselineObserveTrigramContinuationTotalIsBounded`, `TestInMemoryObserveConcurrentTrigramTracking` (`internal/store/store_test.go`), `TestFileStoreSurvivesRestartWithTrigramState` (`internal/store/file_test.go`) in [`internal/baseline/baseline_test.go`](../internal/baseline/baseline_test.go); `TestScoreNGramRarityColdStart`, `TestScoreNGramRarityNeverExceedsBounds`, `TestDefaultConfigNGramWeightIsOptIn` in [`internal/anomaly/anomaly_test.go`](../internal/anomaly/anomaly_test.go); `TestAnalyzeNGramCrossActorIsolation`, `TestAnalyzeNGramScoresBeforeLearning`, `TestObserveNGramLearnsOnlyFromEligibleDecisions` in [`engine_test.go`](../engine_test.go) |
| Markov surprisal — zero-probability safety, correlated-signal double-counting, poisoning, actor isolation (`v0.6` task 028) | `TestScoreMarkovSurprisalUnseenTransitionNeverFires`, `TestScoreMarkovSurprisalNeverExceedsBounds`, `TestScoreMarkovSurprisalColdStart`, `TestMarkovSurprisalIsMonotonicReparameterizationOfRarity`, `TestScoreMarkovAndTransitionRarityAreMutuallyExclusiveInScoring`, `TestScoreCombinedMarkovAndNGramSignalsRemainBounded` in [`internal/anomaly/anomaly_test.go`](../internal/anomaly/anomaly_test.go); `TestAnalyzeMarkovCrossActorIsolation`, `TestAnalyzeMarkovScoresBeforeLearning`, `TestObserveMarkovLearnsOnlyFromEligibleDecisions` in [`engine_test.go`](../engine_test.go) |
| AI Agent behavioral context — session-ID cardinality, fingerprint independence, actor isolation, delegation (`v0.7` task 014) | `TestAnalyzeAgentSessionIDDoesNotExplodeBaseline`, `TestAnalyzeAgentToolNoveltyDetectedByExistingEngine`, `TestAnalyzeAgentToolSequenceNoveltyDetectedByExistingEngine`, `TestAnalyzeAgentCrossActorIsolation`, `TestAnalyzeAgentDelegationContextScoredIdentically` in [`engine_test.go`](../engine_test.go); `TestFingerprintIDIndependentOfAgentContext` in [`internal/fingerprint/fingerprint_test.go`](../internal/fingerprint/fingerprint_test.go); `TestEventValidateIgnoresAgentContextFields` in [`event/event_test.go`](../event/event_test.go) |
| Approval-aware policy — policy authority over the requirement, fail-closed missing evidence, backward compatibility, behavioral independence, non-agent genericity (`v0.7` task 030) | `TestEvaluateApprovalRequiredExampleMatrix`, `TestEvaluateApprovalPolicyAuthorityEventCannotOverridePolicy`, `TestEvaluateApprovalFailSafeOnMissingEvidence`, `TestEvaluateNoApprovalRuleConfiguredIsUnaffectedByApprovalStatus` in [`internal/policy/policy_test.go`](../internal/policy/policy_test.go); `TestAnalyzeAgentApprovalPolicyAllowsApprovedDeniesUnapproved`, `TestAnalyzeApprovalPolicyBehavioralScoreIndependence`, `TestAnalyzeApprovalPolicyGenericNotHardCodedToAIAgent` in [`engine_test.go`](../engine_test.go); `TestValidateRejectsUnknownApprovalStatusDoesNotSilentlyMapToApproved` in [`config/validate_test.go`](../config/validate_test.go) |
| Delegation behavioral semantics — novelty detection, cardinality bound, poisoning protection, score-before-learn, actor isolation, approval independence, Fingerprint independence (`v0.7` task 031) | `TestAnalyzeDelegationNoveltyDetectedByExistingSignal`, `TestAnalyzeDelegationScoreBeforeLearn`, `TestAnalyzeDelegationPoisoningIneligibleEventsDoNotTrain`, `TestAnalyzeDelegationActorIsolation`, `TestAnalyzeDelegationMissingDelegationUnaffected`, `TestAnalyzeDelegationFingerprintStability`, `TestAnalyzeDelegationApprovalIndependence` in [`engine_test.go`](../engine_test.go); `TestBaselineObserveDelegatorCountsIsBounded`, `TestBaselineObserveMissingDelegationDoesNotUpdateDelegatorCounts` in [`internal/baseline/baseline_test.go`](../internal/baseline/baseline_test.go); `TestScoreDelegationDeviation`, `TestScoreDelegationDeviationDoesNotAffectFingerprintOrStable` in [`internal/anomaly/anomaly_test.go`](../internal/anomaly/anomaly_test.go) |
| Agent security scenario validation — five scenarios plus a combined case, no correlated-evidence explosion, identity-confidence independence (`v0.7` task 032) | `TestScenarioUnexpectedPrivilegedTool`, `TestScenarioSensitiveExfiltrationSequence`, `TestScenarioApprovalViolation`, `TestScenarioUnexpectedDelegator`, `TestScenarioExternalDestinationDrift`, `TestScenarioCombinedDelegationSequenceApproval`, `TestScenarioCombinedPoisoningRegression`, `TestScenarioIdentityConfidenceStaysIndependent`, `TestScenarioNoCorrelatedEvidenceExplosionUnderEveryWeight`, `TestScenarioSessionCardinalityRegression`, `TestScenarioNonAgentRegressionUnaffected` in [`scenario_test.go`](../scenario_test.go) |

## Threats considered

### Telemetry spoofing

**Threat:** a producer sends fabricated or misleading event data to
manipulate a decision.

**Status: partially addressed, by design boundary.** Trustvian treats
`Actor.IdentityConfidence` as an external input it trusts, never
something it computes (`internal/trust`) — it is explicitly *not* an
authenticator. If upstream telemetry is spoofed with a high
`IdentityConfidence` and no compensating signal, Trustvian will trust
it accordingly; this is a stated boundary, not a gap being hidden.
Verifying the telemetry pipeline itself (mTLS, signed spans, a trusted
collector) is the deploying application's responsibility, upstream of
this engine. **Future work:** none planned in-core; this is inherently
a transport/collection-layer concern.

### Identity confusion

**Threat:** two different actors (or the same actor in different
environments/tenants) collide on the same behavioral history, letting
one actor's baseline apply to another.

**Status: implemented, and verified end-to-end.** `baseline.Key{ActorID,
Environment}` is a composite key from the start, not a bare actor-ID
string — even though the OSS engine is single-tenant today. This is
deliberately cheap now and expensive to retrofit later (see
[ADR 0004](adr/0004-narrow-store-port-in-memory-only.md)). A future
multi-tenant `TenantID` dimension is an additive extension of the same
pattern, not a redesign. This was true by construction from
`baseline.Key`'s shape but not directly proven end-to-end through
`Engine` until `TestAnalyzeCrossActorIsolation` in
[`engine_test.go`](../engine_test.go)
([task 012](archive/tasks/v0.1/012-security-tests.md)): two actors that produce an
otherwise identical stable feature shape (same operation, same target,
same environment) never share `Baseline` state — actor-a is matured
over 30 observations, and actor-b's first-ever event for the exact same
shape still registers zero `Anomaly.Confidence`.

### Replay

**Threat:** a captured, legitimate event is resubmitted to
artificially reinforce a baseline or repeat a decision.

**Status: not implemented; explicitly deferred.** `Event.ID` and
`Timestamp` exist and could support a bounded-window dedup cache, but
this is an adapter/collection-layer concern (where events first enter
the system), not something `Engine.Analyze`/`Observe` currently do —
the core engine is stateless per call except for the `Baseline` it's
explicitly asked to update. **Future work:** dedup at the OTel adapter
or Collector processor layer.

### Baseline poisoning

**Threat:** an attacker (or a persistently misbehaving process)
gradually "trains" the baseline into treating malicious behavior as
normal by repeating it.

**Status: implemented, and verified end-to-end.** `Engine.Observe`
only learns from `Decision`s where the action *proceeded*
(`ALLOW`/`OBSERVE_ONLY`/`ALERT`); anything held or stopped
(`CHALLENGE`/`REQUIRE_APPROVAL`/`BLOCK`) is never folded into the
baseline. This gating lives inside `Observe` itself, not in caller
discipline — it's safe to call unconditionally after every `Analyze`.
See `TestObserveLearnsOnlyFromEligibleDecisions` and
`TestAnalyzeSensitiveTargetFloorEndToEnd` in
[`engine_test.go`](../engine_test.go), which specifically proves a
`BLOCK`ed, sensitive-destination pattern cannot mature its way to
trust no matter how many times it's repeated.

A related, non-obvious nuance: the eligible-decision set includes
`ALERT`, not only `ALLOW`/`OBSERVE_ONLY`. Excluding `ALERT` created a
real deadlock during development — a brand-new, entirely benign
fingerprint can transiently cross into `ALERT`-level risk purely from
partial maturity, and if that state were ineligible for learning, it
could never mature past it. See `eligibleForLearning`'s doc comment in
[`engine.go`](../engine.go).

**A second, structurally different poisoning path: skewing an EWMA
with a single allowed-but-extreme input.** The gating above answers
"can repeating *blocked* behavior wear the system down?" — no. It does
not, by itself, answer "can one *permitted* observation move a learned
statistic further than it should?" `FingerprintStats` keeps three
exponentially-weighted moving averages (latency, inter-observation
interval, error rate) with `emaAlpha = 0.2`, so a single sample moves
the mean by 20% of its distance and decays only gradually. A learning-
eligible outlier therefore has real, if bounded and self-correcting,
influence — that is the intended cost of EWMA decay (absorbing
legitimate drift without a manual reset), not a defect. Two things
follow:

- **The interval EWMA's unbounded variant is closed.** Before the v0.1
  final-review pass, an event whose `Timestamp` preceded the
  fingerprint's `LastObserved` — clock skew, out-of-order delivery, or
  a deliberately backdated event from an untrusted producer — folded a
  *negative* interval into `IntervalMean`/`IntervalVariance`. That is
  not a bounded outlier: one backdated event can drive `IntervalMean`
  arbitrarily far negative (a 30-day backdate measured
  `IntervalMean = -143h59m52s`), which makes every subsequent on-time
  event look anomalous; and one legitimate long gap inflates
  `IntervalVariance` enough to desensitize the frequency signal to a
  genuine burst that follows. Such an event is normally decided
  `observe_only`, i.e. fully learning-eligible, so decision gating never
  saw it. `FingerprintStats.observe` now skips interval tracking
  entirely for any non-positive interval and advances `LastObserved`
  monotonically, and `anomaly.Score` refuses to fire
  `frequency_deviation` on a negative interval as the read-side half of
  the same guard. Proven by
  `TestFingerprintStatsIgnoresNonPositiveInterval` and
  `TestFingerprintStatsOutOfOrderObservationDoesNotDistortNextInterval`
  in [`internal/baseline/baseline_test.go`](../internal/baseline/baseline_test.go),
  and by `TestScoreFrequencyDeviation`'s
  `negative interval does not fire` subtests in
  [`internal/anomaly/anomaly_test.go`](../internal/anomaly/anomaly_test.go).
- **The bounded variant remains, by design.** An extreme but
  *forward-in-time* latency, interval, or error observation that a
  policy permits still moves its EWMA, in both the latency and interval
  statistics. No per-sample clamp or outlier rejection is implemented,
  and adding one would trade away the decay property the EWMA exists
  for. The mitigating factors are that the influence is bounded by
  `emaAlpha` and decays as normal traffic resumes, that
  `Anomaly.Confidence` is reported separately so a thin baseline is
  never mistaken for a confident one, and that
  `SensitiveTargetFloor`-gated risk cannot be learned away at all.
  **Future work:** if a deployment needs it, per-sample outlier
  rejection belongs in `FingerprintStats.observe` alongside the
  ordering guard, not in caller discipline.
- **`HourActivity` (task [017](archive/tasks/v0.3/017-baseline-time-patterns.md))
  is the same bounded, self-correcting shape, not a new threat class.**
  `FingerprintStats.HourActivity` is an EWMA-of-indicator per UTC
  hour-of-day, structurally identical to `ErrorRate` above except
  applied to 24 buckets instead of one. A single allowed-but-unusual-
  hour observation nudges its bucket the same bounded, decaying amount
  the interval/latency/error EWMAs already tolerate — no new poisoning
  primitive is introduced. It ships with `anomaly.Config.TimePatternWeight
  = 0` (see [DOMAIN.md § Anomaly](DOMAIN.md)), so this signal
  contributes nothing to `Anomaly.Score` in the default configuration
  regardless.

**Persistence-adjacent note:** `store.FileStore`
([ADR 0006](adr/0006-file-backed-persistent-store.md)) flushes to disk
synchronously after every `Observe`, so an unclean shutdown loses at
most the single in-flight observation, never a corrupted or
partially-written file (writes are atomic via temp-file-plus-rename).
This does not introduce a new poisoning vector: the gating above
applies identically regardless of which `Store` implementation is
configured — `FileStore` persists exactly what `Observe` already
decided was eligible to learn, nothing more. Restarting a process using
`FileStore` resumes from the same (gated) baseline it had before the
restart, rather than the empty one `InMemory` would present — this is
the intended fix for the "every restart quietly forgets an attacker's
prior flagged behavior" gap an in-memory-only store would otherwise
leave.

### Learning-scope selection

**Threat:** an event producer chooses which learned behavioral history it
trains — appending benign behavior to a profile a later policy decision
depends on, or steering its own traffic into a fresh profile to escape an
established baseline. Either is [baseline
poisoning](#baseline-poisoning) reached by a different route: not by
wearing down a profile, but by picking a different one.

**Status: prevented structurally.** `v1.0`'s learning scope
([ADR 0024](adr/0024-learning-scope-is-a-baseline-key-dimension.md)) is
`Engine` configuration, supplied once at construction through
`trustvian.WithLearningScope`. There is no path from event content to
`baseline.Key.Scope`:

- `Event` has no scope field, and none is planned. An event describes an
  observed action; a scope describes how one Engine organizes history.
- No scope is derived from `Context.SessionID`, `Context.TraceID`,
  `Context.DelegatedFrom`, or `Attributes` — explicitly ruled out in
  [ADR 0022](adr/0022-core-platform-boundary.md) before the mechanism was
  designed, and asserted by `TestEventContentCannotSelectScope` in
  [`learning_scope_test.go`](../learning_scope_test.go), which submits
  attacker-shaped events carrying `learning_scope`, `candidate_id`, and a
  forged session and trace, and checks the analysis still lands in the
  engine's configured scope with its trained history intact.
- `Engine.Observe` writes to `Result.BaselineKey` — the key `Analyze`
  actually read — rather than recomputing a scope from event metadata, so
  the write cannot be redirected after the fact either.

The trust boundary is therefore between *whoever configures the engine* and
*whoever emits events*, which is the same boundary
[telemetry spoofing](#telemetry-spoofing) already assumes. A caller that can
construct the `Engine` can already choose everything else about it.

**What this is not:** an authorization boundary. A scope carries no access
control, and any code holding an `Engine` can name any scope. It partitions
learning, not permission — see [future multi-tenant
isolation](#future-multi-tenant-isolation).

### Platform control-plane input

**Threat:** the control-plane domain (`platform/`, task 052) accepts
caller-supplied strings — identifiers, names, candidate metadata, failure
reasons — that will eventually arrive over an API and land in storage.
Unbounded or malformed values there become a resource problem, a log-injection
problem, or two identities a human reads as one.

**Status: bounded at construction.** One policy covers every string in the
domain: at most 256 bytes (the same limit `config` already applies to public
rule names), valid UTF-8, and no control characters — C0, C1, or DEL. Control
characters are rejected because they survive no boundary intact: a newline
splits a log line, a NUL truncates a C string, and an escape sequence rewrites
the terminal an operator is reading.

Identifiers additionally reject leading and trailing whitespace. `"cand-1 "`
and `"cand-1"` are different map keys and different database rows, and
accepting both manufactures two identities that look like one.

The shape carries as much of the guarantee as the limits do. There is no map,
no `any`, no raw payload field, and no prompt, completion, or tool-argument
field anywhere in the domain — candidate metadata is a fixed set of optional
descriptive fields, so its total size is bounded by construction rather than
by a counted limit. Event history is deliberately absent and belongs behind a
later capability boundary.

**What this is not:** an authorization boundary. A `Project` is a workspace,
not a tenant, and carries no access control. Multi-tenancy remains
[unimplemented](#future-multi-tenant-isolation).

### Platform aggregation treats engine evidence as untrusted input

**Threat:** the control plane aggregates `DecisionRecord` values that did not
come from a healthy engine — hand-constructed, tampered with in transit, or
belonging to a different evaluation — and reports a confident summary built on
them.

**Status: fail-closed at the aggregation boundary.** `DecisionRecord` is a
detached public struct, so a caller can build or modify one freely. The
aggregator therefore validates every field it consumes, **before any state
changes**, and a rejected record leaves the aggregate byte-identical: no
partial count, no advanced time range.

Malformed input is **refused, never repaired**. Nothing clamps a non-finite
score or coerces an unrecognized decision into a default bucket. The core
guarantees its own output, so a record failing these checks did not come from
a healthy engine, and rewriting it would convert a corruption signal into a
plausible-looking number.

Specific boundaries worth naming:

- **Cross-environment contamination is refused.** A record whose environment
  differs from the evaluation's is rejected rather than counted. The record's
  own `Behavior.Environment` must agree with it too — for genuine engine
  output they are the same value, so a disagreement means the record was
  assembled rather than produced.
- **A policy rule name is not severity.** `block-prod-shell` proves nothing
  about criticality, and nothing parses one. There is no per-rule map, which
  would also be keyed by a caller-controlled value and therefore unbounded.
- **Approval aggregation is evidence counting, not authorization.**
  `ApprovalStatus` is supplied by whoever produced the event. Counting a
  `Denied` is not finding a violation, and no field reads like one —
  interpreting approval evidence needs policy context that belongs to the
  scorecard and gate tasks.
- **Sensitivity is not inferred.** No public rule says what `ContextRisk`
  makes an action sensitive, so the aggregator does not invent one.
- **No raw payload is retained.** No event, no attributes, no contributors
  list, no prompts or tool arguments — the aggregate has nowhere to put them.
- **Duplicates count twice.** Deduplication needs a set of every identifier
  seen, which is unbounded; replay belongs to an ingest boundary that can
  define a retention window. Stated rather than silently true.
- **State stays fixed-size** as the record count grows, so a long evaluation
  cannot exhaust memory through the aggregate.
- **Diagnostics are bounded too.** `DecisionRecord` strings are unbounded
  public input, so rejection paths echo only a 64-byte preview of any
  untrusted value: malformed evidence cannot force an error or a log line to
  reproduce the entire value. Quoting is applied to the truncated prefix
  rather than the whole string, so the allocation is bounded as well as the
  output, and truncation is visible rather than silent.
- **Policy-selection evidence cannot be fabricated.** `MatchedDefault` and
  `PolicyRule` are validated as one pairing — a matched rule names itself, the
  default names nothing — so a hand-built record cannot claim a rule matched
  while naming none.
- **An aggregate belongs to exactly one valid run**, at both ends. A
  zero-value `EvaluationRun` produces no aggregate, and a zero-value
  *aggregate* accepts no record — unexported fields prevent mutation, not
  construction, so both zero values are writable from any package. Left open,
  an unbound aggregate would have matched records whose environment was also
  empty and accumulated evidence belonging to no run.

See [ADR 0026](adr/0026-evaluation-aggregation-is-bounded-evidence.md).

### Behavioral comparison is bounded and refuses partial evidence

**Threat:** a behavioral diff consumes attacker-influenced evidence and either
grows without limit, or — worse — reports a confident answer derived from
evidence that stopped early. The headline output is *which behaviors are new*,
so a quietly truncated comparison under-reports additions in exactly the runs
whose behavioral surface is widest.

**Status: bounded by construction, and loud when it cannot answer.**

- **Distinct behaviors are capped at 512** per collector. The set is keyed by
  `FingerprintID`, which the core derives from caller-supplied fields — the
  same hazard [ADR 0019](adr/0019-bounded-fingerprint-admission.md) bounded in
  the engine. The platform declares its own constant rather than importing the
  core's, so the two may diverge.
- **Every retained string is bounded** to 256 bytes — fingerprint, operation
  name, target name — and the enumerated dimensions are validated against the
  public `event` constants. A capped entry count is not a bound if each entry
  can hold an arbitrarily large string.
- **Identity is never truncated, only rejected.** Two different behaviors must
  not become one because a display string was shortened; a merged pair would
  under-report exactly what the diff exists to report. Truncation is for
  diagnostics.
- **The 513th distinct behavior cannot be silently dropped.** It is refused,
  and the collector becomes permanently incomplete — subsequent observations
  fail rather than continuing to build a partial picture that reads like a
  whole one.
- **An incomplete snapshot cannot produce a diff.** Comparison refuses it
  outright. An error a caller must handle is strictly better than a plausible
  wrong number nobody questions.
- **Fingerprint identity and behavior descriptor must agree one-to-one, both
  ways**, in the collector and again during comparison. One fingerprint with
  two shapes would report two behaviors as one. One shape under two
  fingerprints is worse: classified naively it becomes `Removed(old)` +
  `Added(new)`, a specific and confident claim that behavior changed when only
  its identity encoding did — and "added behavior" is the output most likely
  to be acted on. The platform never recomputes the core's hash to check this;
  it verifies only that the evidence it was handed is self-consistent, so the
  fingerprint algorithm stays free to change.
- **Comparison requires matching environments.** Environment is a stable
  fingerprint dimension, so a cross-environment diff would classify every
  behavior as simultaneously added and removed.
- **Duplicates count twice**, with no identifier set: dedup is unbounded by
  construction and belongs to an ingest boundary with a retention window.
- **No raw event history is retained** — no record, event, contributor,
  attribute, timestamp list, or identifier survives into a snapshot.
- **Platform identity never becomes behavioral identity.** Actor, candidate,
  run, session and profile references are excluded from the comparison key by
  construction, not by convention.
- **A diff is evidence, not a decision.** It reports presence and frequency
  and applies no threshold. Whether a change is acceptable is a gate's
  question, and putting it here would make the component that counts facts
  also the one that renders verdicts.

### Scorecards combine evidence, and refuse to combine the wrong evidence

**Threat:** a scorecard assembles three pieces of evidence that do not
describe the same comparison, and presents the result as one evaluation —
a decision distribution from one run beside a behavioral summary from
another, or from a different environment, or from two partial views of one
stream.

**Status: every correspondence verified before anything is built.** Each
aggregate is matched against its *own* side of the diff (run, candidate and
behavioral profile), all three must agree on the environment, and both
observation counts must match:

```text
reference.RecordCount == diff.ReferenceObservationCount
candidate.RecordCount == diff.CandidateObservationCount
```

That last check catches the subtle case. Tasks 053 and 054 consume the same
stream through two reducers; if one saw 100 records and the other 91, the two
halves describe different evaluations and no reconciliation is correct. It is
refused rather than best-efforted, and there is no partial card.

Other properties worth naming:

- **Zero-value evidence is not empty evidence.** A zero `EvaluationAggregate`
  has all-zero counts — indistinguishable *by value* from a valid empty
  evaluation, and opposite in meaning. Both zero aggregates and zero diffs
  are refused; a genuinely empty evaluation produces a valid card.
- **Empty denominators are undefined, never zero.** Every rate, delta, mean
  comparison and presence ratio reports availability. Collapsing "nothing was
  observed" into `0.0` always errs toward looking safe: a candidate that ran
  nothing would otherwise show a zero block rate, a zero critical-risk rate,
  and perfect behavioral overlap — an ideal candidate that did not run.
- **Unsupported security semantics are absent, not zero.** No
  `CriticalPolicyViolations`, `BlockedSensitiveActions`,
  `UnapprovedSensitiveActions`, per-rule compliance, or delegation stability
  field exists. `PolicyRule` carries no severity and is not retained;
  `ContextRisk` has no repository-defined sensitivity threshold;
  `ApprovalStatus` is producer-supplied evidence; and the aggregate keeps no
  per-event rule, resource, or delegation correlation. A field reporting `0`
  for any of these would be a false security claim that reads as a
  measurement which found nothing rather than one that never ran.
- **No overall score exists,** and no weights. A composite would let strength
  in one dimension offset a critical condition in another — which the
  roadmap's own rule forbids — and would become the number callers read
  instead of the gate.
- **No average may gate.** Categorical counts are integer arithmetic and
  order-independent; mean comparisons inherit task 053's floating-point
  semantics and are not guaranteed bit-identical under reordering. Critical
  conditions must rest on counts.
- **No raw history is retained.** No event identifier, record, delta,
  fingerprint, or rule name reaches a card, and its shape is fixed.
- **Availability is not permission.** Candidate block and critical-risk
  counts are visible so a gate *could* use them; whether any of them may
  legitimately gate a promotion is task 056's decision, not this layer's.
  Task 056 gates both, under their factual names, and invents no severity or
  sensitivity contract to go with them.

### Hard gates fail closed on missing evidence

**Threat:** a candidate passes a promotion gate without having demonstrated
anything — either because it produced no evidence at all, because the policy
that judged it was never configured, or because a favourable average offset a
condition the gate exists to catch.

**Status: every path fails closed.**
[Task 056](tasks/v1.0/056-deterministic-hard-gates.md) pairs a
scorecard with explicit caller-owned limits and returns PASS or FAIL.

- **A zero or unbound gate policy is rejected** with `ErrInvalidGatePolicy`.
  This matters more than the usual zero-value guard, because `0` is a
  legitimate limit: `MaxBlockDecisions = 0` accepts no block decision at all.
  If zero also meant "unset", the strictest policy and an absent policy would
  be the same value, and the failure direction would be toward permissive.
- **All-zero limits are valid and strict** when they come from
  `NewEvaluationGatePolicy`. A private marker separates the two cases, so
  configuring the strictest possible policy is never mistaken for configuring
  none.
- **A zero or unbound scorecard is rejected** with `ErrInvalidGateEvidence`.
  Task 055 added the card's marker for exactly this check; validity is never
  inferred from public zero values.
- **Valid empty evidence fails rather than appearing safe.** A candidate that
  ran zero records has zero blocks, zero critical risks and zero added
  behaviors, and satisfies every maximum. Two non-configurable sufficiency
  gates require at least one record on each side. An empty evaluation is not
  an input error — it is evidence that fails the gate, and those two outcomes
  are kept distinct.
- **Every gate is an integer comparison.** No mean, rate, delta, or presence
  ratio takes part. Floating-point sums are not guaranteed bit-identical
  under record reordering, so a float gate could flip on replay of the same
  evidence.
- **No value can compensate for a failed gate.** There is no weighting model
  to disable: PASS requires all five checks, and the absence of a
  compensating path is what makes "a high average cannot override a critical
  violation" true by construction.
- **Every check is evaluated on every call.** There is no short-circuit, so a
  FAIL reports everything measured rather than everything up to the first
  problem.
- **FAIL is a normal verdict, not an exception.** An error means the
  evaluation itself could not be trusted — unbound inputs, or structurally
  impossible evidence such as a negative behavior count, which converts to
  `math.MaxUint64` — a value that fails every ordinary limit but is accepted
  by the permissive `MaxUint64` one a caller indifferent to a gate is told to
  configure, turning corrupted evidence into a clean PASS.
- **Unsupported severity and sensitivity gates are absent, not zero.** No
  critical-policy-violation, sensitive-resource, or approval-compliance gate
  exists, because no evidence supports one. Approximating any of them from an
  available count would produce a gate whose name promises a guarantee its
  evidence cannot support.
- **A verdict performs nothing.** PASS means only that the configured task
  056 gates passed. It is not an approval, a promotion, or a deployment, no
  field is named for one, and nothing acts on the result.

### Local persistence fails closed and stores no raw history

### An evaluation run cannot name an environment nobody registered

**Threat:** a mistyped environment reference silently creates a run whose
evidence describes a population of one, and every comparison bounded by that
reference inherits the mistake without anything being positioned to notice.

Before [task 065](tasks/v1.0/065-environment-model.md) an `EnvironmentRef` was
validated for shape and nothing else. `ControlPlane.CreateEvaluationRun` now
resolves the run through candidate → agent → project and requires the named
environment to exist in that project and be active, failing with
`ErrStoreNotFound` or `ErrEnvironmentUnavailable` **before** the run is
written and before any realtime event is published. A reference that exists in
a *different* project does not exist for this run: identity is
`(ProjectID, EnvironmentRef)`.

`ControlPlane.CompareEvaluations` gained the matching precondition. The
existing rule that two runs must share an environment reference was standing in
for "the same environment", and project-scoped references made that a hazard —
two projects may each own a `staging`, and comparing across them would produce
a scorecard over unrelated populations while reporting the environment as
`staging`. Both runs must now resolve to one project, checked before any
evidence is loaded.

Neither check runs on the ingest path: a record is never resolved against the
registry, and a run already underway is unaffected by its environment being
archived afterwards. Environment configuration governs what may start, never
what is running or what has finished — and there is no delete, so a completed
run's reference always resolves. An environment stores no URL, credential,
secret or deployment target, and the platform opens no connection on one's
behalf.

### A recorded promotion decision cannot be forged, rewritten, or re-derived

**Threat:** the promotion record is the platform's audit answer to *who
decided this candidate could advance, on what evidence, and when*. Four ways
that answer goes wrong: a caller asserts the outcome instead of the gate
deciding it; the stored evidence is recomputed on read, so a later code change
silently rewrites what the platform "relied on"; the decision commits against
an environment configuration that changed underneath it; or a migration
fabricates decisions nobody made.

**Status: all four are closed, and the closures are tested rather than
asserted.** See
[ADR 0040](adr/0040-promotions-are-immutable-evidence-backed-platform-decisions.md).

- **The outcome is derived from the gate verdict, never supplied.**
  `PromotionDecision` has no `Outcome` field, strict decoding rejects one in a
  request body, and `outcomeFor` is the single place `accepted ↔ PASS` is
  written. An `accepted` promotion carrying a FAIL result is unconstructible
  through the domain; such a row is reported as corruption on read rather than
  repaired, because repairing it would pick one of two contradictory claims
  about a security decision and serve the guess as the record.
- **The gate result is historical evidence, stored field for field.** The
  promotion holds the exact `EvaluationGateResult` the decision consumed —
  every count, every limit, every per-check `Passed` flag, and the verdict —
  restored by `restoreEvaluationGateResult`, which calls neither
  `EvaluateEvaluationGate` nor the gate helpers and derives no flag from its
  own stored operands. A corrected gate may legitimately answer the same
  immutable evidence differently later, and history has to survive its own bug
  fixes. A row that disagrees with today's arithmetic — `actual = 1`,
  `minimum = 1`, `passed = false` — restores exactly as written; only a row
  that cannot be read at all is corruption.
- **The decision commits only against the state it was decided against.** The
  invariant spans three rows, so `CreatePromotion` re-reads both environments
  inside its own transaction, compares both stored revisions, re-asks
  `CanPromote` on the fresh values, and only then inserts. Any change,
  including a rename, yields `ErrStoreConflict` and writes no row — the store
  does not decide which environment changes are "relevant to promotion",
  because that judgment is domain logic and would need revisiting whenever
  `Environment` gains a field. PostgreSQL holds the two environment rows with
  `FOR UPDATE` in `(project_id, ref)` byte order — two rows, never the project
  row and never the table. SQLite serializes writers database-wide instead,
  which is stated rather than dressed up: no test claims row-level writer
  concurrency there.
- **Migration invents no history.** The v3 → v4 step creates the table and its
  index and stamps the version. It writes no rows, and both backends' migration
  tests assert an empty promotion history. Every completed evaluation in a v3
  database was gated by something, but nobody decided to advance any of them;
  synthesizing promotions from old gate results would fabricate an audit trail
  of decisions that were never made.

**A promotion is not a deployment, and nothing pretends otherwise.** There is
no `Deployment`, `Release`, `CurrentEnvironment` or `DeployedCandidate`, no
webhook and no pipeline trigger; the WebUI and CLI carry absence tests
forbidding "deployed to", "now running in", "is live in", "released to" and
"rolled out" as descriptions of a promotion. Trustvian has no observer that
could confirm a deployment happened, so any such claim would be one it cannot
substantiate.

There is also **no `approved_by` and no actor identity**. Task 066 records what
the platform decided on evidence, not who asked, and authentication is its own
milestone. A nullable or client-supplied actor label would be worse than none:
an audit field nobody authenticates can say anything, and its presence invites
readers to treat it as attested.

Records are **append-only**. There is no update and no delete at any layer, so
a decision made in error is superseded by a later decision and both remain
visible. Both gate verdicts are recorded — a FAIL produces a stored `rejected`
promotion — because the gate limits are caller-owned, and a history of
acceptances only would hide a caller retrying with progressively looser limits
until one passed.

**Threat:** durable platform state becomes a way to forge evidence, adopt a
database nobody vetted, silently rewrite what a finished run was evaluated
against, or accumulate the event history this design deliberately does not
keep.

**Status: every path is closed at the adapter boundary.**
[Task 057](tasks/v1.0/057-local-platform-persistence.md) adds SQLite behind
narrow capabilities — there is no generic `Database` interface, and no
`Query`/`Exec`/`Put(any)` a caller could aim anywhere.

- **Caller input reaches SQL only as a bound parameter.** Every identifier,
  name, metadata string, failure reason, environment, profile, fingerprint,
  operation and target name is a parameter; only compile-time table and column
  names appear in assembled SQL. A project named
  `agent'; DROP TABLE platform_projects; --` round-trips as data.
- **Foreign keys enforce ownership** — agent→project, candidate→agent,
  run→candidate, evidence→run. `PRAGMA foreign_keys` is per-connection, so the
  connection model is constrained to make that true for every statement rather
  than for whichever connection happened to run a pragma, and a test asserts
  the database itself refuses an orphan.
- **Create never overwrites.** A duplicate identity returns an error and
  leaves the stored row untouched, even when the incoming value differs. The
  case that matters is a `CandidateID` arriving with a different artifact
  digest: silently rewriting it would change what a finished run was evaluated
  against.
- **The store generates no platform identity.** No autoincrement domain ID, no
  `last_insert_rowid`, no generated UUID. An identity minted by storage is one
  nobody chose, and it would differ between two backends holding the same
  logical data.
- **Unknown, ambiguous and incomplete schema states fail closed.** A version
  this build does not support is refused. So are recognized tables with no
  version metadata — stamping those as fresh would silently adopt data this
  code has never seen. So is metadata claiming v1 beside a schema missing
  required tables: a version row is a claim, not proof, and accepting a
  partial restore moves the failure from open to the first write. Nothing is
  recreated; there is no repair migration. Schema creation and version
  recording commit together, and a racing initializer is resolved by
  re-inspecting the committed schema rather than by matching an error string.
- **Aggregate and snapshot update atomically.** They are two views of one run;
  written separately, an aggregate from observation N could commit beside a
  snapshot from N−1 and become durable truth. One transaction covers the
  aggregate, the header and every entry.
- **Restored evidence is validated before it is trusted, and bound back to
  its run.** Category totals, metric counts and ranges, finite floats, entry
  uniqueness, the behavior-to-fingerprint relation in both directions,
  environment agreement and exact observation sums are all re-proved before
  the private bound marker is set — and the restored pair must then match the
  persisted `EvaluationRun`. Two halves agreeing with each other proves only
  that they were edited consistently. Half a pair present is corruption, never
  "no evidence yet": treating it as first-write state would let the next save
  complete the record and erase every trace that anything went wrong, so it is
  refused on read and on save alike. The stored completeness flag is parsed as
  exactly 0 or 1, because reading it as `== 1` would normalize a corrupt value
  into the safer-looking answer.
  Corrupt rows return an error and no partial value; nothing is clamped or
  repaired, because a patched metric produces evidence that looks measured and
  is invented. Runs are rebuilt by replaying their domain transitions, so a
  corrupt chronology fails the same invariant a live value would.
- **Full `uint64` counters are never narrowed.** SQLite `INTEGER` is signed
  64-bit, so `int64(value)` would corrupt everything above `MaxInt64` —
  silently, and only for large values. Counters are canonical base-10 text,
  round-tripped across the whole `0 … MaxUint64` domain and tested at the
  boundary.
- **Raw event history is intentionally absent.** No table grows per event. No
  `DecisionRecord`, `Event`, diff, scorecard, gate result or gate policy is
  stored; the derived values are recomputed from evidence so there is one
  source of truth. An allowlist test fails if such a table ever appears.
- **Core baseline state is not duplicated.** `baseline.Key`, learned
  fingerprints, sequence and delegation state stay in the engine's own stores.
  Two learned-state authorities would silently change the engine's durability
  model.
- **Snapshot capacity stays 512**, and **incomplete evidence stays
  incomplete**: a saturated snapshot persists as a diagnostic, restores with
  `Complete() == false`, is still refused by `CompareBehaviorSnapshots`, and
  cannot be rewritten as complete by a later write. Saturation is a fact about
  what was observed.
- **Stale evidence cannot overwrite newer evidence.** A lower observation
  count is refused, an identical rewrite is idempotent, and two divergent
  views at the same count are refused rather than silently resolved.
- **There is no listener.** Task 057 adds no network surface, no server, no
  port, and no background goroutine. It is a file.

### The local control-plane API bounds what it accepts and reveals

**Threat:** a network-facing surface becomes a way to smuggle payloads past
the privacy boundary, corrupt evidence through ordinary retries, learn about
the server from its errors, or quietly acquire capabilities nobody reviewed.

**Status: every path is bounded or refused.**
[Task 058](tasks/v1.0/058-local-control-plane-api-and-ingest.md) adds a
service layer and an HTTP adapter over it.

- **The body is bounded before it is decoded.** 256 KiB, applied with
  `MaxBytesReader` ahead of parsing — a limit checked afterwards has already
  spent the memory it was meant to protect. Oversized requests are `413`, and
  both the boundary and one byte past it are tested.
- **Raw event payloads are impossible by schema.** Ingest accepts
  `DecisionRecord`, which structurally excludes event attributes, tool
  arguments, prompts and completions. There is deliberately no
  `metadata map[string]any` on the envelope: that field would undo task 050's
  privacy boundary on its first use.
- **No route analyzes a raw `Event`.** The control plane is not a second
  engine host, so it trains no baseline and makes no behavioral decision.
- **The behavioral profile is bound to the run.** It travels beside the
  record and must match the run's profile, so evidence produced under one
  learning scope cannot be folded into an evaluation of another.
- **Evidence is accepted only while a run is `Running`, checked inside the
  commit transaction.** A pending run has not started, and a terminal one's
  evidence is what a comparison reads. The service also prechecks, but a
  completion committing between that check and the write would otherwise let
  evidence land after the run ended — so the transactional check is the
  authoritative one, and a completed evaluation's evidence can never grow.
- **A monotonic sequence prevents duplicate aggregation.** Task 053 made a
  duplicate record count twice, so an ordinary HTTP retry would otherwise
  corrupt the evidence. Only the expected sequence applies; an identical retry
  of the previous one replays without re-aggregating.
- **Identical concurrent retries all succeed.** A client retrying a slow
  request sends the same sequence and record, and several may pass preflight
  before any commits. The commit transaction distinguishes "the cursor moved
  because somebody committed *this* record" from "the cursor moved for another
  reason": the first replays, the second conflicts. One applies, the aggregate
  advances once, and the counts in every reply come from the transaction that
  decided it.
- **Every ambiguous retry fails closed.** The same sequence with a different
  record, a stale sequence, and a gap are all conflicts. A migrated task 057
  run has no recorded digest, so nothing can be proven identical and a retry
  of its last sequence conflicts rather than guessing.
- **Sequence and evidence commit in one transaction.** Split, the failure
  modes are silent: evidence without the cursor makes the next retry
  double-count, and the cursor without evidence loses a record the protocol
  believes arrived.
- **The cursor is O(1) per run**, not history: a next sequence and a digest.
  There is no EventID set, no idempotency table, and no stored record — raw
  event history remains a separate capability with its own retention and
  privacy questions.
- **Cursor and evidence must agree**, and a read cannot invent a
  disagreement. A cursor that disagrees with the aggregate's record count, or
  exists without evidence, is refused as corruption rather than reconciled by
  guessing which side is right — and multi-query reads run in one transaction,
  so they never observe a snapshot from after a write beside an aggregate from
  before it.
- **A persisted cursor always records a digest.** An empty one is legitimate
  only for a run migrated from task 057, which has evidence but no cursor row
  and no digest to derive. A row with an empty digest could not have been
  written by this code, so it is corruption. Persisted digests are exactly 64
  lowercase hex characters, refused rather than normalized.
- **Internal errors are sanitized.** Storage corruption is a `500` with a
  generic message, never a `400` a client might try to "correct". No SQL text,
  database path, driver message or stack trace reaches a response body, and a
  test asserts the absence of each.
- **A zero-record evaluation fails its gates rather than disappearing.** Such
  a run persists no evidence, and reporting its comparison as "not found"
  would hide the one candidate shape task 056's mandatory minimum-evidence
  gates exist to catch — one that satisfies every maximum because it observed
  nothing. The service materializes empty evidence and the ordinary gate logic
  produces the FAIL. It does so only when the durable cursor agrees nothing
  was written: a cursor reporting records beside missing evidence, or a
  partial pair, remains corruption.
- **HTTP computes no business logic.** Handlers call one service method and
  map the result; a source-scanning test fails if the adapter ever references
  the comparison, scorecard or gate constructors, or a store type.
- **No realtime, and no listener.** No SSE, WebSocket, pub/sub or polling
  loop; requests are synchronous. The adapter binds no port — composing a
  server is a later task, and it must default to loopback.
- **No CORS wildcard**, because there is no browser caller yet. A permissive
  origin with no consumer is a permission granted to nobody in particular.
- **No authentication or access control is claimed.** Local `v1` mode does not
  require it, and inventing users, tokens, sessions or roles here would be a
  security model nobody reviewed. That is a later task's decision, and this
  API should not be exposed beyond loopback until it is made.

### Realtime is bounded, ephemeral, and never authoritative

**Threat:** a notification stream becomes a way to leak payloads the record
boundary excludes, exhaust memory through subscribers or queues, let one
client stall every other, or quietly become a source of truth clients rely on
instead of the database.

**Status: bounded at every dimension, and advisory by construction.**
[Task 059](tasks/v1.0/059-realtime-infrastructure.md) publishes only after a
committed mutation, over an in-memory bus with no history.

- **No raw event payload.** An observation is a bounded projection —
  fingerprint, behavioral shape, decision, risk, approval and three numeric
  signals. No `Event.Attributes`, tool arguments, prompts, completions,
  contributors, `PolicyReason` or raw record, so task 050's boundary holds on
  this path. A test drives a real engine with a distinctive attribute value
  and asserts it never reaches the wire.
- **Per-subscriber queues are bounded** at a fixed capacity, and **the
  subscriber count is bounded** too — a bounded queue with unlimited
  subscribers is still unbounded memory, so worst-case cost is a constant
  rather than a function of who connected. At the limit a new subscription is
  refused; an existing one is never evicted to admit it.
- **Slow consumers are disconnected**, not blocked and not silently trimmed.
  Blocking would add a stalled client's latency to every committed mutation;
  dropping silently would leave that client believing it has a complete stream
  when it does not. Disconnection makes the gap observable while the client
  can still act on it.
- **Filters are applied before enqueue**, so a busy run cannot fill the queue
  of a subscriber watching a quiet one. Without that the per-subscriber bound
  would be nominal.
- **Filter input is validated by the bus, before a slot is taken.** Nothing
  else checks it — an unmatched filter is a legitimate subscription to a quiet
  stream, so no store lookup stands between a caller's string and the bus.
  Each non-empty dimension obeys the same identifier rule as every other
  platform value (256 bytes, valid UTF-8, no control characters, no
  surrounding whitespace). Validating in the SSE handler instead would leave
  the bus reachable, unchecked, by any in-process caller; validating after
  registration would let malformed input exhaust the subscriber bound with
  identifiers the bus was about to reject. A malformed filter is a `400`
  `invalid_request`, never a `503` — an SSE client’s reflex on a `5xx` is to
  reconnect against a request that will never succeed.
- **One subscriber cannot stall another.** Publishing to one never waits for
  another, and a dead subscriber affects only itself.
- **No durable replay history exists.** Nothing is retained after delivery,
  no offsets are kept, and `Last-Event-ID` is ignored rather than honored —
  accepting it while unable to replay would promise durability the bus does
  not have. No SSE `id:` field is emitted.
- **Every connection requires resynchronization**, and the handshake arrives
  *after* the subscription is registered, so nothing that happens during the
  resync is lost.
- **Delivery failure never falsifies a durable outcome.** A committed write
  reports success even when the bus is closed, broken or full. The alternative
  makes a client retry a write that landed, and ambiguity about whether a
  write happened is worse than a missed notification.
- **Publication happens after the commit**, never before, so a subscriber
  cannot observe state that never existed. Failed, conflicted and replayed
  operations publish nothing.
- **No message broker, no CORS, no authentication claim, no listener, and no
  event-history table.** Realtime inherits task 058's local trust boundary
  exactly; nothing here is authenticated and nothing claims to be.
- **An SSE disconnect releases its subscription**, so a client that hangs up
  does not hold a slot.
- **Every SSE delivery attempt has a finite deadline** — the write *and* the
  flush behind it — refreshed immediately before it, covering the handshake,
  every domain frame and every heartbeat. `Write` can succeed into a local
  buffer while the flush is what reaches the socket, and `http.Flusher.Flush`
  returns nothing, so flushing goes through `ResponseController.Flush` and its
  error is propagated rather than discarded. A write or flush failure
  terminates **only that stream** and releases its subscription; nothing is
  retried, buffered or dropped, because once one frame is uncertain the client
  cannot be assumed to have it. The bus
  bounds what it queues; it does not bound a client that holds a healthy
  subscription and stops draining its socket. That client’s queue never fills,
  so it is never disconnected — and the handler blocks inside `Write` where it
  can no longer observe its context, its subscription, or a shutdown, so the
  stalled connections accumulate outside every count the bus keeps. The
  deadline is refreshed per write rather than set once (set once it is a
  connection lifetime, killing healthy streams), is separate from the
  heartbeat interval (a slower keepalive must not buy a stalled client more
  time), and is configurable only within a fixed maximum, rejected rather than
  clamped outside it. A writer that cannot take a deadline is refused with a
  sanitized `500` before any `200`, because continuing unbounded is precisely
  the condition the deadline prevents.

### The developer CLI is a client, and claims nothing more

**Threat:** a CLI that drives the control plane becomes a second way in —
reaching the database directly, recomputing a gate verdict, generating
identity, leaking credentials through a URL, or turning a network failure into
a reported policy result that a CI job then acts on.

**Status: an HTTP adapter with no authority of its own.**
[Task 060](tasks/v1.0/060-developer-cli.md) consumes the `/v1` API and nothing
else; see [ADR 0033](adr/0033-developer-cli-is-a-thin-http-adapter.md).

- **The API URL is explicit.** `--api-url` is required on every control-plane
  command. No default address or port is chosen, and no environment variable
  is read — task 062 owns integrated local startup, and a default frozen
  before the thing that binds it exists is far harder to change later.
- **Credentials in the URL are rejected**, and the diagnostic does not repeat
  the value. A URL is the wrong place for a secret, and a refusal that echoed
  it into shell history, CI logs and terminal scrollback would have protected
  nothing.
- **No authentication is claimed.** No `--token`, no credential store, no
  auth header. Task 070 owns authentication; a flag now would freeze a
  credential shape before there is anything to authenticate against. A test
  fails the build if such a flag is declared.
- **Redirects are not followed.** A mutation addressed to one host must not
  silently become a mutation against another; a redirect is refused with an
  operational exit and the destination receives nothing.
- **Request bodies are bounded** at the server's own 256 KiB limit, checked on
  the encoded envelope rather than the input file, so the CLI never knowingly
  builds a request the server must reject.
- **Response bodies are bounded** at 4 MiB, read as limit+1 and failed closed.
  A client that reads an unbounded body trusts the server's good behavior for
  its own memory safety.
- **The request timeout is finite** — 30s, a fixed constant. There are no
  retries: a GET is safe to repeat but a lifecycle POST is not, and ingest
  already carries explicit sequence semantics, so retrying is the caller's
  decision.
- **Identifiers are never generated.** Every create requires an explicit
  `--id` ([ADR 0025](adr/0025-platform-domain-values-with-caller-owned-identity.md)),
  and caller-owned IDs are URL-escaped into paths, so an identifier cannot
  reshape a route or traverse one.
- **Unknown `DecisionRecord` fields survive ingest.** The record is
  transported as raw JSON and never decoded into the CLI's current struct: a
  round trip would strip a newer producer's field *before transmission*,
  defeating the server's deliberate tolerance from the client side, silently.
- **Unknown API response fields are tolerated and preserved.** Decoding never
  uses `DisallowUnknownFields`, and `--json` forwards the server's own body
  rather than a re-marshalled subset.
- **No direct database access.** No `database/sql`, no SQLite driver, no
  `--db` flag, no path to the platform store. The store's invariants live in
  `ControlPlane`, and a second writer reaching past them is how a store
  acquires states its own code believes impossible.
- **No listener is bound.** The CLI is a client; task 062 owns the local
  runtime. A test fails the build on `ListenAndServe` in shipped CLI source.
- **No work outlives a command.** No background goroutine, no cache, no retry
  state, no sequence cursor written to disk.
- **API errors never masquerade as gate failure.** For the control-plane
  families, exit `3` covers every transport, timeout, redirect and HTTP error;
  exit `1` means a gate FAIL, and only on `trustvian eval compare`. This is
  the load-bearing one: CI scripts are habitually written as "non-zero means
  the gate failed", and conflating the two would report a broken network as a
  policy violation — or teach a team to ignore the code that means their agent
  regressed.
- **The CLI computes no verdict.** It reads `gate.verdict` and reproduces
  neither the diff, the scorecard, nor any threshold comparison. A second
  implementation of a security decision does not fail loudly; it agrees for a
  year and then diverges on an untested edge, in the direction of a false
  PASS.

### The terminal dashboard renders untrusted text safely, and holds nothing

**Threat:** a dashboard is the first client that holds a long-lived connection
and the first that paints server-supplied text onto a terminal. So it can
accumulate state nothing bounds, become a second source of truth for a run's
history, or — the one with the sharpest edge — let a server-controlled string
execute terminal control sequences on a developer's machine.

**Status: bounded, read-only, and sanitized.**
[Task 061](tasks/v1.0/061-terminal-dashboard.md) consumes `/v1` HTTP and SSE
and nothing else; see
[ADR 0034](adr/0034-tui-is-a-bounded-realtime-http-client.md).

- **Server-originated text cannot emit terminal control sequences.**
  Identifiers, operation and target names, failure reasons and environment
  refs all come from outside the process and all reach the screen. A terminal
  is an execution surface: ESC moves the cursor, clears the display, retitles
  the window, emits clickable OSC hyperlinks and changes colors. Every such
  string passes through one sanitizer that replaces ESC, BEL, C0, DEL and C1
  before rendering. The framework emits control sequences; data does not. A
  regression drives `"\x1b[2J\x1b]0;owned\x07"` through run metadata,
  behavior descriptors and failure reasons and asserts no server byte survives
  into the view.
- **SSE input is bounded** at 64 KiB per line and per frame, checked while
  reading rather than after. A server sending an endless line costs a fixed
  amount of memory and fails, instead of growing a buffer until something
  dies; a test drives exactly that case against a reader that never ends.
- **The resync buffer is bounded** at 64 frames, matching the server's
  per-subscriber queue. Overflow **abandons the stream and resynchronizes**
  rather than dropping a frame — a client that discarded a notification could
  not describe what it missed.
- **The observation window is bounded** at 100 rows and evicts the oldest.
  That is deliberate truncation of a *display*, and the distinction from the
  transport bound is load-bearing: one would be data loss, the other is a
  viewport.
- **No raw event payload.** The realtime projection excludes attributes, tool
  arguments, prompts, completions, contributors and policy reason, and the TUI
  has no endpoint that would return them. A test sends an unknown observation
  field carrying a marker string and asserts it is neither rendered nor
  retained.
- **No history and no replay.** Nothing is written to disk. Rows are cleared
  on every reconnect, so two disconnected streams are never joined into an
  apparent sequence. `Last-Event-ID` is never sent and no `id:` is retained —
  the bus keeps no history, so a cursor would ask for a replay that cannot
  happen.
- **Reconnect requires an authoritative resync**, and subscription always
  precedes the state fetch. The reverse order loses anything committed between
  the two. A terminal lifecycle event buffered during a resync still triggers
  its one final read, so a completed run never displays stale closing counts.
- **Active-stream silence is finitely bounded** at 60 seconds, four times the
  server heartbeat. A half-open connection delivers no bytes, no error and no
  EOF; without this the dashboard would block forever while continuing to show
  LIVE. `http.Transport.IdleConnTimeout` does not provide it — that governs
  unused pooled connections, not an active body. Heartbeat comments refresh
  the bound, so a quiet healthy evaluation is never disconnected.
- **Known events are validated for wire integrity**: payload version, `kind`
  matching the SSE event name, `scope.run_id` matching the watched run, and
  the payload half the kind requires. An event scoped to another run is
  refused rather than rendered — the subscription is filtered server-side, so
  a mismatch means the filter did not hold, and attributing another agent's
  behavior to this dashboard would be worse than showing nothing. Unknown
  event names remain ignorable and are held to no shape.
- **Abandoning a connection cancels its in-flight work.** Every network
  operation runs under a generation-scoped context, so a reconnect, protocol
  failure or quit cancels the open and the authoritative reads it replaces.
  Generation numbers alone only discarded the results; the sockets and
  goroutines kept running.
- **No polling.** No ticker over `/progress`, no watcher. A quiet healthy
  stream issues exactly one run read and one progress read, which a test
  asserts; the only extra read is one final resync when a terminal lifecycle
  event arrives.
- **Read-only.** Three GETs — the stream, the run, its progress. No lifecycle
  POST, no ingest, no compare, and no keybinding that mutates. An architecture
  test enforces the endpoint list structurally.
- **Credentials in `--api-url` are rejected** and never echoed; no auth header
  is sent and none is claimed. Task 070 owns authentication.
- **No listener is bound**, and the streaming client refuses redirects: a
  subscription addressed to one host must not silently become one to another.
- **Quitting cancels everything.** One context owns the connection, the reader
  goroutine and the reconnect timer; a test asserts the server observes the
  client going away.

### The local runtime is unauthenticated, and loopback is the boundary

**Threat:** a one-command local platform is the moment an unauthenticated
control plane becomes reachable from a network — by a helpful flag, a reverse
proxy, or a discovery file that points somewhere it should not.

**Status: loopback-only, with no way to change that.**
[Task 062](tasks/v1.0/062-integrated-local-developer-workflow.md) composes the
runtime; see
[ADR 0035](adr/0035-local-runtime-composes-platform-without-reversing-modules.md).

- **The listener binds loopback only.** `--listen` accepts numeric loopback
  addresses and nothing else — `0.0.0.0`, `::`, private ranges, public
  addresses and hostnames are all refused. Hostnames are refused even when
  they would resolve to loopback, because resolution is not proof and what
  `localhost` means is the resolver's opinion. There is no `--allow-remote`,
  `--insecure` or `--public`: a flag that exposes an unauthenticated control
  plane is a flag someone will set on a café network.
- **The port is ephemeral.** `127.0.0.1:0` lets the OS choose, so no address
  becomes a compatibility surface and nothing collides.
- **No authentication is claimed or implemented**, and no TLS. This is an HTTP
  server on loopback, described exactly that way rather than dressed in a
  self-signed certificate that would imply more. Task 070 owns authentication
  and remote exposure.
- **Do not reverse-proxy or expose this runtime.** Loopback is the entire
  security boundary; putting anything in front of it removes the only
  protection it has.
- **Discovery is not authentication.** `.trustvian/runtime.json` carries two
  fields — version and endpoint. No token, no secret, no identifiers. Finding
  it proves nothing about who may use the endpoint, and it must never grow a
  credential: a secret in a project directory looks like security without
  being it.
- **A discovered endpoint must be loopback.** A repository can contain a
  `.trustvian/runtime.json`, so a discovered URL is validated more strictly
  than one a user types: `http`, numeric loopback host, no credentials, no
  path, query or fragment. Otherwise checking out a project would be enough to
  redirect a developer's mutation commands to someone else's server. Explicit
  `--api-url` always wins and is never overridden by a file.
- **The discovery file is read under a 4 KiB bound**, checked before opening
  and again while decoding, because a project-local file is not necessarily
  trustworthy.
- **No CORS.** Task 062's clients are a CLI and a TUI, not a browser; task 063
  can define browser-origin requirements against an interface that exists.
- **Platform state is separate from engine state.** `.trustvian/platform.db`
  holds evaluation evidence and never becomes the engine's baseline store —
  different lifecycles, different correctness properties, and a test asserts
  the schemas stay apart.
- **No raw event endpoint.** The producer path stays `Engine.Analyze` →
  `DecisionRecord` → ingest, so no second scoring implementation appears
  server-side.
- **Shutdown releases everything**: the realtime bus closes first so in-flight
  SSE handlers exit, then the server within a finite bound, then SQLite, and
  the discovery file is removed **only if it still names this runtime** — a
  late process must not erase a newer one's endpoint.

### The browser control plane renders untrusted text inertly, and stores nothing

**Threat:** a browser is an execution surface in a way a terminal is not. A
server-supplied string that becomes markup runs as code with the page's
privileges; a page that can reach other origins can carry local control-plane
data off the machine; and a UI that keeps its own copy of platform state becomes
a second source of truth that disagrees with the database.

**Status: text-only rendering, deny-by-default policy, nothing persisted.**
[Task 063](tasks/v1.0/063-minimal-web-control-plane.md) serves the UI from task
062's listener; see
[ADR 0036](adr/0036-webui-is-a-same-origin-adapter-over-v1.md).

- **No server string can create executable DOM.** Identifiers, names, failure
  reasons, candidate metadata, behaviour descriptors, decision and risk strings
  and API error messages all originate outside the process and all reach the
  screen. Every one arrives through `textContent`, and elements are built with
  `createElement`. `innerHTML`, `outerHTML`, `insertAdjacentHTML`,
  `document.write`, `eval` and `new Function` are absent from shipped source,
  enforced by a test that strips comments and string literals before matching —
  the assets deliberately *name* those primitives in prose explaining why they
  are unused, and a guard weakened to tolerate that prose would stop catching
  the real thing. A fixture drives `<img src=x onerror=alert(1)>`,
  `</script><script>alert(1)</script>` and `javascript:alert(1)` through the
  real API as project names and asserts they round-trip as data.
- **The Content Security Policy denies by default.** `default-src 'none'`, with
  `'self'` for script, style, connect and img, and `'none'` for font and object;
  `base-uri 'none'`, `frame-ancestors 'none'`, `form-action 'self'`. It contains
  no `'unsafe-inline'`, no `'unsafe-eval'`, no `*`, no scheme wildcard and no
  external host, which is achievable only because the shipped HTML carries no
  inline `<script>`, no inline `<style>` and no inline event handler. A test
  asserts each of those tokens is absent rather than trusting the string.
- **No external origin is contacted.** No CDN script, stylesheet or font, and
  no npm package, lockfile or bundler. The page works offline, and the
  dependency delta for this task is zero in every module.
- **No CORS header is ever sent.** The UI is same-origin with the API it calls,
  so none is needed — and a permissive `Access-Control-Allow-Origin` is the
  single change that would make an unauthenticated local control plane
  reachable from any page a developer visits. Tests assert no
  `Access-Control-*` header appears on any response, including 404s and the
  SSE stream.
- **Only allowlisted fields are displayed.** Rendering iterates an explicit list
  of field names, never the object that arrived, and no `Object.entries`,
  `Object.keys` or `for…in` walk over a server value exists in shipped source.
  The compatibility contract permits `/v1` to gain additive fields at any time;
  a generic renderer would publish the next one without anyone deciding to.
  Prompts, completions, tool arguments, `Event.Attributes`, `PolicyReason`,
  contributors and the raw `DecisionRecord` are outside the realtime contract
  and are not rendered. A fixture asserts the marker values
  `SECRET_PROMPT_DO_NOT_RENDER`, `SECRET_TOOL_ARGUMENT` and
  `SECRET_ADDITIVE_FIELD` are unreachable.
- **Nothing is stored in the browser.** No `localStorage`, `sessionStorage`,
  `IndexedDB` or cookies. A reload forgets which IDs were open, and the
  control-plane database stays authoritative. A run ID in the URL fragment is
  navigation state and is never read back as fact.
- **Realtime stays bounded and non-authoritative**, inheriting task 059's
  contract: subscription precedes the authoritative read, 64 pending frames
  during a resync with overflow abandoning the stream rather than dropping a
  notification, 100 display rows evicting oldest, rows cleared on every
  reconnect so two streams are never joined into an apparent sequence, no
  `Last-Event-ID` and no replay. Every count displayed comes from the database,
  including the single final read a terminal lifecycle event triggers.
- **A connection that never synchronizes is bounded.** `EventSource` exposes
  heartbeat comments to the network layer but not as events, so a server
  sending only heartbeats would hold a connection open forever while never
  completing the protocol — and no read-idle bound can see that, because bytes
  are arriving. A finite handshake deadline, released only by a validated
  `stream_ready` and never extended by heartbeats, ends it. The page closes the
  socket itself on protocol failure rather than letting the browser's implicit
  retry become the reconnect state machine.
- **Responses are bounded before they are parsed.** The client streams a
  response body under a 4 MiB cap and cancels the reader on overflow rather
  than buffering and measuring afterwards, and refuses to send a request body
  over the server's own 256 KiB limit. A client that read an unbounded body
  would have made its memory safety a property of the server behaving well.
- **uint64 counters are never parsed as numbers.** The API encodes them as
  decimal strings because JSON numbers cannot represent them, and this is the
  one place a browser client is more exposed than the CLI: JavaScript has no
  integer type that holds `18446744073709551615`. They stay text end to end,
  gate limits are validated as canonical decimal syntax, and a test forbids
  `parseInt`, `parseFloat`, `Number` and unary-plus coercion on those fields.
- **The server-side package holds no authority.** `webui.NewHandler` takes no
  arguments and cannot be handed a `ControlPlane`; its dependency graph
  contains no platform package, no root module and no database driver. It
  serves embedded bytes and sets headers, and a test asserts the constructor's
  parameter list is empty so the mutation that grants it authority fails.
- **The UI cannot decide anything.** Gate verdicts come from
  `response.gate.verdict`; no arithmetic on limits exists in shipped source, and
  a test scans for the comparison shapes that would constitute recomputation.
  Lifecycle legality is the server's — the page shows the actions and renders
  the refusal. An operational failure is never presented as a gate FAIL.

**Not claimed:** CSP is browser hardening, not authentication. It bounds what
the page may do and says nothing about who may open it. The runtime remains
unauthenticated and loopback-only, and
[task 070](tasks/v1.0/) owns remote authenticated access.

### The shared PostgreSQL backend keeps credentials and internals off every surface

**Threat:** a shared database introduces a connection string, and a connection
string is a secret that wants to escape — into a log, a startup banner, an error
message, a discovery file, an HTTP response. It also introduces a driver whose
errors carry table names, constraint names, SQLSTATE codes and sometimes the SQL
itself, none of which a caller should ever see. And it removes the property that
made the local store's read-then-write operations atomic, which was holding one
connection.

**Status: redacted, mapped, and locked per run.**
[Task 064](tasks/v1.0/064-postgresql-platform-backend.md) adds the backend; see
[ADR 0037](adr/0037-postgresql-is-the-shared-platform-persistence-backend.md).

- **The DSN never appears anywhere.** Not in a log, not in `trustvian-local`'s
  startup output, not in an error, not in `.trustvian/runtime.json`, not in a
  `/v1` response, not in the CLI or the WebUI. There is a specific driver
  behaviour behind this and the engine's store already carries the same
  regression test: pgx redacts the password when it can *parse* a DSN and
  reproduces the string **verbatim** when it cannot — so a mistyped DSN is the
  dangerous case, and it is the one an operator is most likely to produce.
  `redactDSNError` therefore reports the action and nothing derived from the
  input. Tests drive a distinctive sentinel through four DSN shapes at the store
  level and four more at the runtime level, asserting it appears in no error and
  that no wrapper adds the configuration back.
- **The DSN is read from the environment, not a flag.** A command line is
  visible to every process on the machine through `ps`; the engine's store takes
  the same value from a config file for the same reason. Neither puts a
  credential on argv.
- **Backend selection fails closed.** An unrecognized backend name is refused
  rather than defaulted, and PostgreSQL selected without a DSN fails before the
  listener binds. Nothing ever falls back from an explicit selection: a
  deployment that asked for a shared database and silently got a local file would
  look healthy while losing every other process's state.
- **No usable endpoint is advertised before the database is proven.** The pool is
  constructed, connectivity is proven with a `Ping`, and the schema is created,
  verified or migrated — all before the listener binds and before `runtime.json`
  is written. A test asserts a failed PostgreSQL start leaves no reachable
  endpoint behind.
- **Driver internals do not reach `/v1`.** Failures map by SQLSTATE — never by
  message text, because a driver's English is not a contract — onto the same
  sentinels the SQLite store already used: `23505` to already-exists, `23503` to
  not-found, `40001`/`40P01` to conflict, anything else to an unavailability
  class. A runtime-level test drives the real `/v1` surface on PostgreSQL and
  asserts no response contains a SQLSTATE, a table or column name, a constraint,
  raw SQL, the DSN, a host or a username. Cancellation stays the context's own
  error rather than becoming a conflict, so a caller is never told to retry
  something they abandoned.
- **Every value is a bound parameter.** No SQL is assembled from caller input;
  the only runtime-built SQL is a placeholder list and an upsert assignment list
  derived from a package-level column list. The one dynamic identifier anywhere
  is a test-only schema name built from a Go test's name through a constrained
  alphabet. A source guard fails the build on SQL built from a value.
- **Concurrency is designed rather than inherited.** SQLite's atomicity came
  from `SetMaxOpenConns(1)`; a pool has none, so every run-scoped write takes
  `SELECT … FOR UPDATE` on its run row before the reads it depends on, and the
  lifecycle update additionally carries its expected status as a predicate with a
  `RowsAffected` check. The ingest cursor is reached through the run row because
  `FOR UPDATE` locks nothing when the cursor row does not yet exist. Isolation
  stays `READ COMMITTED`: raising it globally would make serialization failures a
  routine outcome and push retry logic onto every caller.
- **A schema this binary does not understand is refused.** Newer than
  `SchemaVersion`, or recognized tables with no version row, both fail closed
  rather than being rewritten — the safe reading of the second is "something else
  wrote here", not "empty". Create-and-stamp is one transaction, so
  tables-without-version is unreachable rather than merely unlikely, and
  concurrent startup is serialized by a transaction-scoped advisory lock whose
  key is proven distinct from the engine store's.
- **Least privilege.** The platform's database role needs DML on its own
  `platform_`-prefixed tables, plus DDL only in the process that migrates. TLS is
  configured through the DSN (`sslmode`, `sslrootcert`) and nothing defaults it
  to `disable`.

**Not claimed:** this adds no authentication, no RBAC and no TLS policy — task
070 owns those. And two control-plane processes sharing one PostgreSQL database
share authoritative state but **not** realtime notifications: each keeps its own
in-process bus, which is a documented limitation of task 064 rather than a
defect. Task 069 owns cross-node realtime.

### Platform identity cannot become behavioral identity

**Threat:** an evaluation concept leaks into the engine — a candidate becomes
an actor, a run becomes a fingerprint dimension, or a git SHA becomes
behavior. The consequence is not a leak but a detector that no longer works:
every deployment would look like a brand-new actor, and the learning the
product exists to accumulate would reset on each release.

**Status: structurally prevented, and checked.** The platform is a separate Go
module whose path (`trustvian-platform`) is deliberately outside
`github.com/trustvian/trustvian`, so Go's import-path ancestry rule makes a
core `internal/*` import a compile error rather than a convention. The core
declares no dependency on the platform in either direction.

`scripts/check-platform-boundary.sh` enforces both directions in CI, as
[ADR 0022](adr/0022-core-platform-boundary.md) requires: no core `internal/*`
import in platform source, no platform package in the core's build graph, and
no platform identifier declared in core runtime code.

An event producer cannot supply a platform identifier either: `event.Event`
has no field for one, and none is derived from `SessionID`, `TraceID`, or
`Attributes` — the same trust boundary [learning-scope
selection](#learning-scope-selection) already draws one layer down.

### Sequence state

**Threat:** `v0.6`'s [transition-deviation
signal](archive/tasks/v0.6/025-sequence-analysis-foundation.md) introduces the
first runtime state whose size is driven not just by an actor's
*current* fingerprint but by pairs of them — a new resource-exhaustion
surface, and a new place cross-actor or ordering mistakes could leak
information between unrelated identities.

**Status: implemented, with specific, tested mitigations** — see [ADR
0010](adr/0010-bounded-process-local-sequence-state.md) for the design
these follow from:

- **Memory exhaustion.** `FingerprintStats.PredecessorCounts` is capped
  at 64 distinct predecessor entries per destination fingerprint
  (`maxPredecessors`) — an attacker who varies the *previous* action on
  every call (trivial, since `Fingerprint.ID` derives from
  caller-controlled `Event` fields) cannot grow one entry's map without
  bound. Once at the bound, a genuinely new predecessor is not added;
  tracked entries keep accumulating normally — proven under real
  concurrent contention by
  `TestInMemoryObserveConcurrentTransitionTracking` in
  [`internal/store/store_test.go`](../internal/store/store_test.go),
  and directly by `TestBaselineObservePredecessorCountsIsBounded` in
  [`internal/baseline/baseline_test.go`](../internal/baseline/baseline_test.go).
  `Baseline.Fingerprints` itself remains unbounded, as it already was
  before this task (see [Resource exhaustion](#resource-exhaustion)
  below) — this task does not newly introduce that characteristic, only
  bounds the one genuinely new structure it does add.
- **High-cardinality identities.** Unaffected beyond the existing
  `Baseline.Fingerprints` characteristic: sequence state adds two
  scalar fields (`LastFingerprintID`, `LastFingerprintTime`) per
  `baseline.Key`, not per identity value observed — no new
  cardinality-sensitive structure.
- **Cross-actor contamination.** Structurally impossible by
  construction, not merely policy: every new field lives inside
  `Baseline`, which is already scoped to exactly one
  `baseline.Key{ActorID, Environment}` and stored behind
  `internal/store`'s existing per-`Key` sharded lock. There is no
  code path where one actor's `LastFingerprintID` or
  `PredecessorCounts` could be read or written while processing a
  different actor's event.
- **Out-of-order events.** `Baseline.Observe` only records a transition
  — and only advances `LastFingerprintID`/`Time` — when the incoming
  timestamp strictly follows the previous one, the identical guard
  `FingerprintStats.observe`'s interval statistics already use for the
  identical reason (see Baseline poisoning above): a backdated or
  replayed event must not (a) be scored as following a predecessor it
  didn't actually follow in real time, or (b) silently rewrite what the
  *next* legitimate event's transition is measured against. Proven by
  `TestBaselineObserveOutOfOrderEventDoesNotRecordOrCorruptTransition`
  and the `out-of-order event does not fire` case in
  `TestScoreTransitionDeviation`.
- **Sensitive history retention.** `PredecessorCounts` stores only
  `Fingerprint.ID` strings (already-hashed, stable-feature identifiers
  — see [DOMAIN.md § Fingerprint](DOMAIN.md)) and integer counts —
  never raw event payloads, attributes, or any sensitive field value.
  No new sensitive data is retained beyond what `Baseline.Fingerprints`
  already holds.
- **Process-local scope, no distributed guarantee claimed.** This state
  does not survive across instances any differently than the rest of
  `Baseline` does: `InMemory` does not survive a restart, `FileStore`
  does (see the Persistence-adjacent note above) — no new claim about
  cross-instance consistency is made or implied. See ADR 0010 § "Why
  process-local."
- **Cold-start safety.** A never-before-seen transition scores as
  maximally novel, matching `categorical_novelty`'s own philosophy for
  a never-before-seen fingerprint — and is equally not, by itself,
  treated as a critical attack: `anomaly.Config.TransitionWeight`
  defaults to `0` (proven by `TestDefaultConfigTransitionWeightIsOptIn`),
  the identical "ships opt-in" mechanism `FrequencyWeight`/
  `TimePatternWeight` already established.

**`v0.6` task 026 (`transition_rarity`) extends this same threat model
with one new counter, `FingerprintStats.OutgoingTransitionTotal`, and
inherits every mitigation above unchanged rather than needing new ones:**

- **No new cardinality dimension.** `OutgoingTransitionTotal` is one
  `uint64` scalar added to the existing `FingerprintStats` entry — it
  does not add a map, and the pre-existing `maxPredecessors = 64` bound
  on `PredecessorCounts` is completely unaffected. An attacker varying
  the previous action on every call gains nothing new to exhaust beyond
  what task 025's own bound already closes.
- **Counter overflow.** `OutgoingTransitionTotal++` is a plain,
  unguarded increment — the same choice `FingerprintStats.Count` already
  made — deliberately not saturating: reaching `2^64` through legitimate
  per-event increments is not a realistic concern for any deployment's
  actual lifetime, and adding saturation logic for only this one field
  while every sibling `uint64` counter in the same struct lacks it would
  be an inconsistent special case, not a genuine safety improvement. See
  [ADR 0011 § Consequences](adr/0011-transition-rarity-statistic-and-orientation.md#consequences).
- **Cold-start / insufficient-sample uncertainty.** A frequency computed
  from a handful of observations is evidence of too little data, not
  evidence of rarity — `transition_rarity` does not fire at all below
  `OutgoingTransitionTotal_A >= Config.MinTransitionObservations`
  (default `20`, matching `MinObservations`'s own precedent), the
  identical "insufficient history is not evidence" stance
  `frequency_deviation`'s `IntervalObservations == 0` gate already
  takes. Proven by the cold-start table in
  `TestScoreTransitionRarityColdStart`. Below the gate, an attacker
  cannot force a misleadingly extreme rarity reading by keeping a
  predecessor's outgoing-transition count artificially low.
- **Baseline-poisoning resistance, inherited, not rebuilt.** Repeating a
  transition that gets `BLOCK`ed never grows
  `OutgoingTransitionTotal`/`PredecessorCounts`, for the same reason
  task 025's own counters are already immune: `Engine.Observe`'s
  pre-existing `eligibleForLearning` gate (see [Baseline
  poisoning](#baseline-poisoning) below) only learns from `ALLOW`/
  `OBSERVE_ONLY`/`ALERT` decisions, and this gate is upstream of *every*
  `Baseline.Observe` call, including the new counter's — task 026 added
  no new learning path for an attacker to target. Proven by
  `TestObserveTransitionRarityLearnsOnlyFromEligibleDecisions`.
- **Actor/environment isolation, inherited.** `OutgoingTransitionTotal`
  lives inside the same `baseline.Key{ActorID, Environment}`-scoped
  `FingerprintStats` every other learned field already uses — no new
  code path crosses actors. Proven by
  `TestAnalyzeTransitionRarityCrossActorIsolation`.
- **Unseen vs. rare stay distinguishable.** `transition_deviation` and
  `transition_rarity` are mutually exclusive by construction
  (`count == 0` vs. `count > 0`), so an operator (or an automated
  policy) can never mistake "this has genuinely never happened" for
  "this happens, just rarely" — the two carry materially different
  security implications and are never collapsed into one signal. See
  [ADR 0011 § Preserving the unseen/rare
  distinction](adr/0011-transition-rarity-statistic-and-orientation.md#preserving-the-unseenrare-distinction).

**`v0.6` task 027 (`ngram_deviation`/`ngram_rarity`) extends this same
threat model one step further back, with two new bounded maps and one
new scalar, and again inherits every mitigation above rather than
needing new ones:**

- **Cardinality explosion, independently bounded — a genuine
  correctness subtlety this task's own review caught.**
  `TrigramCounts` (on the destination) and `TrigramContinuationTotal`
  (on the immediate predecessor) each carry their own explicit
  `maxTrigramPredecessors` (64) cap. It would be tempting to assume
  `TrigramContinuationTotal`'s cardinality is bounded "for free" by
  `PredecessorCounts`'s existing cap — it is not:
  `Baseline.Observe`'s history-window shift
  (`PreviousFingerprintID` advancing) happens unconditionally on every
  valid advance, regardless of whether `recordPredecessor` actually
  admitted the corresponding key into `PredecessorCounts` or rejected
  it for being past *that* map's own bound. An attacker varying the
  grandparent fingerprint on every call could otherwise grow either new
  map without bound even while `PredecessorCounts` itself stays capped.
  See [ADR 0012 § Why `TrigramContinuationTotal` needed its own
  bound](adr/0012-bounded-trigram-behavioral-context.md#why-trigramcontinuationtotal-needed-its-own-bound-not-an-inherited-one),
  proven by `TestBaselineObserveTrigramCountsIsBounded` and
  `TestBaselineObserveTrigramContinuationTotalIsBounded`.
- **Counter overflow.** Both new maps' counters use a plain, unguarded
  increment (`recordTrigram`/`recordTrigramContinuation`), matching
  `PredecessorCounts`/`OutgoingTransitionTotal`'s own established
  precedent — deliberately not saturating, for the identical reason:
  reaching `2^64` through legitimate per-event increments is not a
  realistic concern for any deployment's actual lifetime, and a
  special case for only these two counters would be inconsistent with
  every sibling counter in the same struct.
- **Cold-start / insufficient-sample uncertainty.** `ngram_rarity` does
  not fire at all below
  `TrigramContinuationTotal_B[A] >= Config.MinNGramObservations`
  (default `20`) — the identical "insufficient history is not
  evidence" stance `transition_rarity`'s own gate already takes, applied
  one level up. Proven by the cold-start table in
  `TestScoreNGramRarityColdStart`.
- **Baseline-poisoning resistance, inherited, not rebuilt.** Repeating
  a 3-gram that gets `BLOCK`ed never grows
  `TrigramCounts`/`TrigramContinuationTotal`, for the identical reason
  task 025/026's own counters are already immune — `Engine.Observe`'s
  pre-existing `eligibleForLearning` gate sits upstream of *every*
  `Baseline.Observe` call, including these two new maps'; task 027
  added no new learning path for an attacker to target. Proven by
  `TestObserveNGramLearnsOnlyFromEligibleDecisions`, using the exact
  scenario this section's own threat model implies: a normal
  `authenticate -> read -> update` path and a malicious, `BLOCK`ed
  `authenticate -> export -> delete` path that 50 replayed attempts
  never normalize.
- **Actor/environment isolation, inherited.** `PreviousFingerprintID`
  and both new maps live inside the same
  `baseline.Key{ActorID, Environment}`-scoped `Baseline`/`FingerprintStats`
  every other learned field already uses — no new code path crosses
  actors. Proven by `TestAnalyzeNGramCrossActorIsolation`, which checks
  not merely that no signal leaks but that a fresh actor's identical
  3-gram reads as *maximally* novel (`Value == 1`), the precise
  condition that would be violated by any leakage from another actor's
  history.
- **Unseen vs. rare stay distinguishable, one level up.**
  `ngram_deviation` and `ngram_rarity` are mutually exclusive by
  construction, for the identical reason
  `transition_deviation`/`transition_rarity` already are.
- **History-retention scope, unchanged.** `PreviousFingerprintID` is a
  single `Fingerprint.ID` string (already-hashed, stable-feature
  identifier), not a growing window — no more raw history is retained
  per actor than task 025 already introduced, just one more fixed
  pointer.

**`v0.6` task 028 (`markov_surprisal`) adds no new state at all — it
reads task 025/026's own `PredecessorCounts`/`OutgoingTransitionTotal`
fields — so it inherits every mitigation above (bounds, poisoning
resistance, actor isolation, ordering) automatically, with two
considerations specific to this signal's own arithmetic:**

- **Zero probability, made structurally impossible, not merely
  guarded.** `markov_surprisal` never evaluates a transition whose
  count is zero — that remains `transition_deviation`'s domain (task
  025) — so `-log2(0)` (`-Inf`) is never computed at all, not
  clamped after the fact. Combined with the shared
  `MinTransitionObservations` gate (below which no signal fires),
  `frequency` is always in `(0, 1]` whenever this signal's arithmetic
  runs, making `surprisal` always finite and `normalized` always in
  `[0, 1)`. Proven by `TestScoreMarkovSurprisalUnseenTransitionNeverFires`
  and `TestScoreMarkovSurprisalNeverExceedsBounds` (the latter across
  totals up to 10,000, checking `!IsNaN`/`!IsInf`/bounds explicitly on
  every reading, not just the typical case).
- **Correlated-signal amplification, prevented structurally, not by
  convention.** `transition_rarity` and `markov_surprisal` are
  mathematically proven (see [ADR
  0013](adr/0013-first-order-markov-surprisal-without-duplicate-evidence.md))
  to be monotonic reparameterizations of the *identical* frequency
  statistic — scoring both independently would double-count one piece
  of evidence as if it were two, inflating `Score` beyond what the
  underlying evidence actually supports. `anomaly.Score` forces
  `transition_rarity`'s own contribution to zero whenever
  `Config.MarkovWeight > 0`, regardless of what
  `Config.TransitionRarityWeight` is separately set to — a caller
  cannot double-count this evidence by any combination of the two
  weight fields, because the code itself enforces the exclusion, not
  documentation alone. Proven by
  `TestScoreMarkovAndTransitionRarityAreMutuallyExclusiveInScoring`
  (`Score` with both weights set is bit-for-bit identical to `Score`
  with only `MarkovWeight` set). `markov_surprisal` *can* legitimately
  co-fire with task 027's `ngram_deviation`/`ngram_rarity` on the same
  event — those answer a genuinely different question (a specific
  two-fingerprint predecessor pair, not the same first-order frequency)
  — and `combine()`'s existing clamped noisy-OR keeps that combination
  bounded regardless, proven by
  `TestScoreCombinedMarkovAndNGramSignalsRemainBounded`.

### Malicious agents / privilege escalation

**Threat:** an AI agent or service account uses legitimate credentials
to access something outside its normal scope (secrets, admin
interfaces), and — because access to a sensitive destination is itself
learnable behavior — gradually normalizes that access.

**Status: implemented.** `anomaly.Config.SensitiveTargetFloor` sets a
fixed minimum anomaly contribution for specific destinations that
persists *regardless of baseline familiarity* — full maturity does not
erase it (see [DOMAIN.md § Anomaly](DOMAIN.md#anomaly) and the same
`TestAnalyzeSensitiveTargetFloorEndToEnd` test above). This is what
directly prevents the "train the baseline into trusting me" attack
path for destinations an operator has explicitly flagged as sensitive.
**Caveat:** this requires the operator to configure
`SensitiveTargetFloor` for the destinations that matter — it is not
automatic classification. See [ROADMAP.md](ROADMAP.md) for automatic
sensitivity detection as a possible future direction.

### Policy bypass

**Threat:** a missing, empty, or misconfigured policy silently
resolves to `ALLOW`, defeating enforcement without anyone noticing.

**Status: implemented.** `policy.Policy.Evaluate` fails closed to
`BLOCK` — not `ALLOW` — when `DefaultAction` is unset or invalid, or
when `DefaultAction` is set but `DefaultReason` is empty. There is no
code path from "the config is wrong" to a silent allow; this is
verified directly by
`TestEvaluateFailsClosedOnZeroValuePolicy`,
`TestEvaluateFailsClosedOnInvalidDefaultAction`, and
`TestEvaluateFailsClosedOnEmptyDefaultReason` in
[`internal/policy/policy_test.go`](../internal/policy/policy_test.go).

### Configuration-input validation

**Threat:** a config-time typo silently *weakens* a policy rather than
visibly breaking it. Unlike a malformed `Event` (rejected immediately
at the boundary) or an entirely unconfigured `Policy` (fails closed to
`BLOCK`), a `PolicyCondition` field with a typo'd value — `actor_type:
srevice` instead of `service` — compiles into a perfectly well-formed
`policy.Condition` that simply never matches anything. The rule
silently stops firing; nothing about evaluation itself looks wrong, so
an operator who believes a rule is active has no signal it never was.

**Status: implemented**
([task 019](archive/tasks/v0.5/019-policy-config-model.md), `config` package).
`(PolicyConfig).Validate()` — called unconditionally inside
`CompilePolicy` too, so this protection cannot be bypassed by skipping
an explicit validation step — rejects, with a field-path-identifying
error: an unsupported/missing schema version, a missing or invalid
default decision/reason, an invalid decision on any rule, an invalid
`actor_type`/`operation_category`/`min_risk_level` on any condition
(including inside `Unless`), an empty or duplicate rule name, an empty
reason, an overlong name, and more than `1000` rules. This is
deliberately *stricter* than `policy.Policy.Evaluate`'s own runtime
fail-closed behavior for exactly this reason: a same-shaped runtime
failure has no way to point back at the config file line that caused
it, while a config-time validation error does. A `PolicyCondition`
with every field left at its zero value is explicitly *not* rejected —
that is legitimate "matches everything" catch-all semantics inherited
from `policy.Condition` itself, not a mistake to flag.

**Resource bounds are deliberately stricter than runtime bounds
elsewhere in this codebase.** `maxRules = 1000` and
`maxNameLength = 256` are far smaller than, e.g., the 100,000-key
`Attributes` map this document's own resource-exhaustion tests already
probe without panicking — a policy config file is human-authored
operational input, not per-event telemetry volume, so a much stricter
cap is appropriate and does not constrain any real use case.

**Out of scope by construction:** `NaN`/`Inf` and numeric-range
validation do not apply to `PolicyCondition` today, since it has no
numeric matcher field (`MinRiskLevel` is a qualitative string enum,
not a float) — `internal/policy.Condition` itself would need a numeric
matching dimension before this validation surface grows to cover it.

**File parsing** ([task 020](archive/tasks/v0.5/020-policy-config-loader.md),
`config.Load`/`config.LoadFile`) introduced the threats a file format
adds beyond `Validate()`'s own checks, all implemented, not deferred:

- **Unknown-field handling is strict, unconditionally.** `Load` uses
  `go.yaml.in/yaml/v3`'s `Decoder.KnownFields(true)` — an unrecognized
  field anywhere in the document (top-level or nested inside a rule's
  `when:`/`unless:`) fails with an error naming it, rather than being
  silently ignored. This is the file-format-level version of the same
  threat the paragraphs above address for value-level typos: a
  field-name typo must not silently produce different security
  behavior than the operator intended. There is no configuration
  option to relax this.
- **Duplicate mapping keys are rejected** — verified as
  `go.yaml.in/yaml/v3`'s own default `Decoder` behavior via a real
  test (`TestLoadRejectsDuplicateYAMLKeys`) against the actual
  library, not assumed from its documentation. A document with two
  `decision:` keys in the same rule fails to decode at all, rather
  than silently taking the first or last value.
- **Input size is bounded.** `LoadFile` reads at most 1 MiB
  (`io.LimitReader(f, maxConfigFileSize+1)`, with the `+1` used to
  *detect* an over-limit file rather than silently truncate and parse
  a partial document) — bounding a pathological input (an
  accidentally-huge file, a special device file) to a fixed, cheap
  read. `Load` itself, given an in-memory `[]byte`, has no additional
  size bound beyond what the caller already chose to hold in memory.
- **Empty input is a distinct, actionable error** (`ErrEmptyInput`),
  not a bare `io.EOF` leaked from the decoder.
- **A fuzz target** (`FuzzLoad`) asserts the one invariant a parser
  handling untrusted input must have: arbitrary bytes never panic
  `Load`. Run with `go test ./config -fuzz=FuzzLoad`.
- **No secret leakage in errors.** Loader/validation errors identify
  field paths and offending values (e.g. an invalid decision string),
  never a signing secret or credential — this loader has no such field
  to leak in the first place (`PolicyConfig` carries no secret-shaped
  data; `AlertConfig`, introduced by task 023 below, doesn't either —
  it has no delivery/credential fields at all, since delivery
  configuration is explicitly out of that task's scope).

### Alert configuration-input validation

**Threat:** the identical config-time-typo threat the section above
addresses for `PolicyCondition`, applied to `AlertConditionConfig`: a
typo'd `severity: crital` or `actor_type: srevice` would otherwise
compile into a well-formed `alert.Condition` that simply never
matches, silently disabling a rule an operator believes is active.
Alert Evaluation's own "no match means no alert" design (see
[Policy bypass](#policy-bypass) above and
`alert.Evaluate`'s doc comment) makes this threat *more* dangerous
here than for Policy, not less — there is no fail-closed floor at
runtime to fall back on if a rule silently stops matching; the
observable failure mode is simply the total *absence* of an alert that
should have fired.

**Status: implemented** ([task
023](archive/tasks/v0.5/023-declarative-alert-configuration.md), `config` package —
`AlertConfig`/`CompileAlerts`/`LoadAlerts`/`LoadAlertsFile`, a
deliberately **separate** document and compilation path from
`PolicyConfig`/`CompilePolicy`, not a shared one — see [ADR
0009](adr/0009-alert-config-is-a-separate-document.md)).
`(AlertConfig).Validate()` — called unconditionally inside
`CompileAlerts`, same non-bypassable guarantee `PolicyConfig.Validate`
already has — rejects: an unsupported schema version, an invalid
`severity` on any rule, an invalid `decision`/`min_risk_level`/
`actor_type`/`target_category` on any condition, an empty or duplicate
rule name, an overlong name, more than `1000` rules (the same bound
`PolicyConfig` uses), and — the one validation surface `PolicyCondition`
explicitly does *not* need (it has no numeric matcher) —
`min_anomaly_score`/`max_trust_score` values that are `NaN`, `±Inf`,
negative, or greater than `1`: `alert.Condition.MinAnomalyScore`/
`MaxTrustScore` are always meant to be compared against
`Anomaly.Score`/`Trust.Score`, both of which are documented to stay
within `[0, 1]`, so a threshold outside that range could never match
anything meaningful and is rejected as a config mistake rather than
silently accepted as a threshold that can never fire or always fires.
Same file-parsing threats as `PolicyConfig` (unknown-field rejection,
duplicate-key rejection, bounded read, `FuzzLoadAlerts`) are covered
identically, reusing the exact same `go.yaml.in/yaml/v3` decoding path
— not a second, parallel parser.

Unlike `PolicyConfig`, `AlertConfig` has no `default_decision`/
`default_reason` to validate — an empty `Rules` list is a legitimate,
if inert, `AlertConfig`, matching `alert.Evaluate`'s own documented
asymmetry with `policy.Policy.Evaluate` (see [Policy
bypass](#policy-bypass)): the *absence* of any alert
rule is a safe, observable no-op, not a security regression the way an
unconfigured `Policy` silently falling open would be.

### Anomaly configuration-input validation

**Threat:** the identical config-time-typo threat the two sections
above address, applied to `AnomalyConfig` — a typo'd
`delegaton_weight` (silently rejected as unknown, not the threat here)
or a `delegation_weight: -1`/`NaN`/`1.5` (which would *not* be a typo
Go's compiler could catch, since these are plain numeric fields) could
otherwise silently disable a signal, silently over-drive `combine()`'s
noisy-OR beyond its documented range, or — for a `NaN` specifically —
propagate an undefined value into `Anomaly.Score` itself.

**Status: implemented** ([task
033](archive/tasks/v0.7/033-v07-stabilization-release-gate.md), `config`
package — `AnomalyConfig`/`CompileAnomaly`/`LoadAnomaly`/
`LoadAnomalyFile`, a third document/compilation path independent of
`PolicyConfig`/`AlertConfig`, per [ADR
0017](adr/0017-public-anomaly-configuration-boundary.md)).
`(AnomalyConfig).Validate()` — called unconditionally inside
`CompileAnomaly`, the same non-bypassable guarantee `PolicyConfig`/
`AlertConfig.Validate` already have — rejects an unsupported schema
version, and, for *every* weight-shaped field (both the plain
`float64` opt-in weights and the three pointer fields `NoveltyWeight`/
`LatencyWeight`/`ErrorWeight`), a value that is `NaN`, `±Inf`,
negative, or greater than `1` — reusing `AlertConditionConfig`'s own
`ErrInvalidThreshold`/`validThreshold` exactly, since both share the
identical documented `[0, 1]` range. The two z-score threshold fields
(`LatencyZThreshold`/`FrequencyZThreshold`) get their own check
(`ErrInvalidZThreshold`: finite and `> 0`), a distinct range from a
weight's — a threshold of `0` would be a division-by-zero risk
downstream, and thresholds legitimately exceed `1` (they are
standard-deviation multiples, not probabilities). Every
`SensitiveTargetFloor` map value is validated identically to a weight.
`MinObservations`/`MinTransitionObservations`/`MinNGramObservations`
have no invalid `uint64` value — `0` is a genuine, internally-handled
configuration, not a threat to guard against. Same file-parsing
threats as `PolicyConfig`/`AlertConfig` (unknown-field rejection,
duplicate-key rejection, bounded read, `FuzzLoadAnomaly`) are covered
identically, reusing the same `go.yaml.in/yaml/v3` decoding path — and
a negative YAML literal into a `uint64` field is rejected by the
decoder itself, verified empirically
(`TestLoadAnomalyRejectsNegativeIntoUnsignedField`), never silently
wrapped into a huge positive value.

**Backward compatibility is the primary security property this task
protects.** `CompileAnomaly(AnomalyConfig{})` — every field at its Go
zero value — must reproduce `anomaly.DefaultConfig()` exactly, not
merely approximately: `TestCompileAnomalyZeroValueMatchesDefaultConfig`
proves this with `reflect.DeepEqual`, because a silent drift here (e.g.
a caller who forgets to set `NoveltyWeight` unintentionally disabling
`categorical_novelty`) would be exactly the "never silently weaken a
security policy" violation CLAUDE.md warns against, just one layer
removed from `Policy` itself.

### Storage configuration and production persistence

**Threat:** an explicitly requested durable store that cannot be built
silently degrades to a non-durable one, and the deployment appears
healthy while learning nothing that survives a restart. This failure
mode is worse than the config-typo threats above: a misconfigured
`Policy` or `AnomalyConfig` produces *different decisions*, which an
operator can observe, whereas a silently substituted store produces
*data loss*, invisible until the restart that needed the data.

**Status: implemented and hardened** ([task
034](archive/tasks/v0.8/034-production-store-contract-and-public-boundary.md), [task
035](archive/tasks/v0.8/035-postgresql-store-implementation.md), [task
036](archive/tasks/v0.8/036-store-durability-concurrency-and-migration-hardening.md),
[ADR 0018](adr/0018-production-store-boundary-and-postgresql-direction.md)).
See [`storage-guide.md`](storage-guide.md) for the operator-facing
version of everything below.

Implemented now:

- **Fail closed, never degrade.** `config.CompileStorage` returns a
  **nil** `Store` on every error path, so a caller who ignores the error
  cannot proceed on a substituted store. Proven by
  `TestCompileStoragePostgresFailsClosedNeverFallsBack` and, at the CLI
  boundary, `TestRunAnalyzeUnreachablePostgresFailsClosed` and
  `TestRunAnalyzeIncompleteStorageConfigFailsClosed` — which additionally
  assert no analysis output is produced at all. Task 035 extended this to
  the case that actually occurs in production: a *reachable-in-config but
  unreachable-in-fact* database. There is no code path from "PostgreSQL is
  unavailable" to a working in-memory or file store. **Silent persistence
  downgrade is prohibited**, because it would mean every subsequent
  decision was made against state the operator believed was durable and
  shared, with the loss discovered long after it mattered. The same
  assertion is made from the external `examples` module
  (`TestPublicConfigPostgresFailsClosedWhenUnreachable`), so it is a
  guarantee to consumers, not an internal convention.
- **No implicit backend.** An omitted `type` is a validation error, not
  a silent choice of the non-durable in-memory store — the same
  fail-closed-on-ambiguous-config discipline `policy.Policy.Evaluate`
  applies to a missing default decision.
- **A misconfigured backend is distinct from an unknown one.** A missing
  `postgres.dsn` returns `ErrMissingStorageDSN` and an unreachable
  database returns a connectivity error — never `ErrInvalidStorageType`,
  which is reserved for a `type` Trustvian does not recognize. An operator
  can therefore tell a typo from a misconfiguration from an outage.
- **Load failures surface at construction.** A corrupt or unreadable
  state file fails `CompileStorage` rather than starting empty and
  silently discarding existing history — `FileStore`'s loader already
  refused unknown snapshot versions rather than misreading them
  (`fileSnapshotVersion`), and that now aborts startup instead of being
  swallowed. Proven by `TestCompileStorageFilePropagatesLoadFailure`.
- **Concurrent update loss is a tested contract, not an assumption.**
  `TestStoreContract`'s same-key-concurrency guarantee requires N
  goroutines × M observations to yield exactly N×M. This is the
  assertion a naive read-then-compute-then-write database
  implementation fails, and it exists before that implementation does.
- **Same config-input hardening as every other document.** Strict
  decoding (unknown fields, duplicate keys), bounded file read,
  `ErrEmptyInput`, and `FuzzLoadStorage` — reusing the identical
  `go.yaml.in/yaml/v3` path, not a second parser.
- **Bounded state unchanged.** Persistence stores current learned
  `Baseline` state only. Every existing cardinality bound
  (`maxPredecessors`, `maxTrigramPredecessors`, `maxDelegators`, all 64)
  applies unchanged, and no raw-event history is persisted — Trustvian
  is not a SIEM or event lake, so there is no unbounded write path and
  no retention policy to get wrong.
- **Privacy unchanged.** No new raw data is persisted: no prompt text,
  tool arguments, secret values, HTTP bodies, or raw SQL. `Baseline`
  holds none of these, and the storage layer persists `Baseline`,
  nothing more.

Implemented by task 035, against the constraints ADR 0018 recorded rather
than left to discretion:

- **Credential leakage.** A DSN contains a password. It is never logged
  and never wrapped into a returned error. One specific hazard was found
  empirically rather than assumed: pgx redacts passwords in parseable
  URL DSNs and in connection errors, but echoes an **unparseable** DSN
  back *verbatim*. `pgxpool.ParseConfig`'s error is therefore deliberately
  **not** wrapped — `ErrInvalidDSN` reports that parsing failed and
  withholds the detail. `TestNewStoreUnparseableDSNDoesNotLeakCredentials`
  is the regression test; the CLI and `examples` fail-closed tests
  additionally assert no password reaches stderr.
- **No credential in persisted state.** Nothing from the storage config is
  serialized into a row. The `baseline` column holds a
  `baseline.Baseline` and nothing else.
- **SQL injection.** Every value is a bound parameter. The only parts of
  any statement assembled from Go strings are the two table-name
  constants in `internal/store/postgres/schema.go`, which are compile-time
  literals — actor IDs, environments, fingerprint IDs, target names,
  operations, and serialized baselines are all parameters. Test-only
  schema names, which cannot be parameterized (PostgreSQL has no
  placeholder for an identifier), are generated locally and checked
  against a strict `^[a-z][a-z0-9_]{0,48}$` whitelist before use.
- **Database unavailable.** Fail fast at startup, as above. No degraded
  mode, no queue, no unbounded internal retry loop hiding a prolonged
  outage — `pgxpool` reconnects within a pool as ordinary pool behavior,
  and Trustvian adds no layer above it. Retry ownership sits with the
  deployment platform.
- **Schema version mismatch fails closed.** A database recorded at a
  different `SchemaVersion` aborts startup with
  `ErrSchemaVersionMismatch` rather than being silently upgraded or
  misread — the same discipline `FileStore` already applied to an unknown
  `fileSnapshotVersion`. Migration is transactional and serialized across
  concurrently starting processes by a transaction-scoped advisory lock.
- **Database privilege scope.** DML on its own two tables; DDL only at
  migration time. Automatic creation means the runtime role needs
  table-creation rights on first run; [`storage-guide.md`](storage-guide.md)
  documents how to split migration from runtime privileges for deployments
  that care.

### Storage hardening (task 036)

Task 035 established these properties; task 036 established that they
hold under the conditions a production deployment actually meets. Each row
is a test, not an intention.

| Threat | Mitigation, verified |
|---|---|
| **Lost updates under contention** | 32 writers × 100 observations at one key, three rounds: every observation present, none lost. 96 concurrent *first* writes at an absent row, five rounds: all present, exactly one row. Task 035's mutation testing already showed this fails loudly when the row lock or the pre-insert is removed. |
| **State corruption via partial write** | A transaction failing immediately before commit (injected with a PostgreSQL trigger) leaves the previous baseline byte-identical. The store remains usable afterwards — a failed transaction does not poison the pool. |
| **Silent behavioral-memory loss** | An unreadable stored baseline produces `ErrCorruptState` on `Observe` and is **never overwritten with a fresh one**. Resetting would erase an actor's learned history and destroy the evidence needed to diagnose it. `Get` reports absence — fail-safe upward, since the actor then reads as unfamiliar — and never writes. |
| **Schema downgrade / unknown-layout mutation** | A recorded schema version newer than the binary fails startup closed. So does an older or unrecognized one. An older binary never mutates state whose layout it does not understand. |
| **Ambiguous schema metadata** | Two silently-accepted gaps found and fixed by this task, both now `ErrAmbiguousSchemaState`: baseline data present with no recorded version (previously stamped with the current version, letting an old binary adopt newer state after a partial restore), and multiple version rows (previously resolved by an unordered `LIMIT 1`, observed accepting a database marked version 99). Recovery is an operator decision; guessing is what the check exists to prevent. |
| **Migration leaving a half-built schema** | Migration runs as one transaction with transactional DDL. An aborted migration is verified to leave no table behind and to damage nothing pre-existing. Twelve simultaneous initializations produce exactly one logical initialization and one version row. |
| **Connection exhaustion / denial of service** | With the pool exhausted, operations wait for capacity and then fail on the caller's deadline — bounded and reportable, never an unbounded hang. Measured: 2.0001 s against a 2 s deadline. A transaction waiting on a row lock is cancellable and returns promptly (5.4 ms). No connection is stranded on any success or failure path, verified against a single-connection pool where one leak would be immediately fatal. |
| **Silent persistence downgrade** | Re-confirmed at three layers. There is no code path from "PostgreSQL is unavailable" to a working memory or file store — not at startup, not after a mid-operation connection loss. A lost connection yields an explicit error wrapping `ErrUnavailable`, and stored state is verified to equal exactly the set of acknowledged writes. |
| **Credential leakage** | The existing redaction regression tests re-run unchanged, and schema-failure paths are additionally asserted not to echo the DSN. |
| **Unbounded growth / event warehousing** | 200 actors × 10 observations yields exactly 200 rows and exactly 2 tables: row count tracks distinct keys, never observation volume. One row's size is bounded by the per-actor caps — at most 512 fingerprints, each with its own bounded inner maps — and a baseline at those caps round-trips without truncation. |
| **Baseline poisoning through persistence** | The learning-eligibility gate lives in `Engine.Observe`, above the `Store`. Forty repetitions of a blocked action are verified to remain ineligible — and to leave `Anomaly.Confidence` at zero — on **all three backends**, so durable shared persistence cannot be used to normalize blocked behavior fleet-wide. |
| **Storage-dependent security decisions** | InMemory, FileStore, and PostgreSQL are verified to produce identical learned state, identical `Anomaly`/`Trust` values, and identical `Decision`s for the same event stream. A backend that straddled a policy threshold differently would turn a BLOCK into an ALLOW; it cannot. |

Two limits stated plainly, because this document should not overclaim:

- **This is not a high-availability guarantee.** Trustvian fails fast when
  its database is unavailable, by design. Availability of the database
  itself — replication, failover, backup — belongs to the deployment, and
  nothing here should be read as providing it.
- **Automatic migration means the runtime role needs DDL rights on first
  run.** Trustvian never needs superuser, and
  [`storage-guide.md`](storage-guide.md) documents how to split migration
  from runtime identity for deployments that want to. The trade-off is
  deliberate: one connection string that works is the smallest safe OSS
  experience.

### Malformed events / extreme input values

**Threat:** a producer sends a structurally valid but adversarial
`Event` — `NaN`/`Inf` `IdentityConfidence`, empty or extremely long
identifiers, negative durations, deeply nested or very large
`Attributes` — attempting to crash the engine, corrupt a score, or
smuggle bad data past validation.

**Status: implemented, with two deliberate exceptions documented as
accepted behavior, not gaps.**

- `Event.Validate()` rejects empty required fields, out-of-range
  `IdentityConfidence` (including `NaN`, `+Inf`, `-Inf`), and invalid
  enum values. The `NaN` case was a genuine, empirically-confirmed gap
  closed by this task: Go's `NaN < 0`/`NaN > 1` are both always false,
  so the pre-existing range check silently passed a `NaN`
  `IdentityConfidence` through `Actor.validate()` (`event/event.go`)
  until an explicit `math.IsNaN` check was added. `+Inf`/`-Inf` were
  already correctly rejected by the range check before this task; see
  `TestValidateRejectsNonFiniteIdentityConfidence` in
  [`event/event_test.go`](../event/event_test.go), which proves all
  three sub-cases individually rather than assuming they behaved alike.
- `Actor.ID` has no length limit, and `TestValidateAcceptsVeryLongActorID`
  in [`event/event_test.go`](../event/event_test.go) asserts that a
  100,000-character `Actor.ID` is accepted, not rejected — this is
  deliberate current behavior per [task 012](archive/tasks/v0.1/012-security-tests.md)'s
  Non-Goals (no length limit added absent a concrete DoS vector), stated
  explicitly rather than left an untested assumption.
- A negative `duration_ms` attribute is not rejected by `Validate` (only
  `IdentityConfidence` and enum fields are checked there) and flows
  through `features.Extract` into `internal/anomaly`'s latency z-score
  math as a negative `time.Duration`. This was traced through
  empirically, not assumed: `(currentNS - mean) / stddev` is a
  well-defined finite division whenever the baseline's `stddev != 0`,
  regardless of the sign of `currentNS`, and `min(z/threshold, 1)` then
  clamps it into the signal's normal `[0,1]` range exactly like any
  other extreme deviation — no `NaN`/`Inf` reaches `Anomaly.Score` or
  `Trust.Score`. `TestAnalyzeNegativeDurationDoesNotCorruptTrustScore`
  in [`engine_test.go`](../engine_test.go) pins this down as a
  regression rather than an implicit assumption; no code change was
  needed here because none was demonstrated necessary.
- `trust.Compute` separately, defensively clamps its numeric inputs to
  `[0,1]` regardless of what's passed (`internal/trust/trust.go`), as a
  second line of defense independent of the above.
- A large `Attributes` map (100,000 keys) does not panic or error
  `Engine.Analyze` — see `TestAnalyzeLargeAttributesMapDoesNotPanic` in
  [`engine_test.go`](../engine_test.go), also listed under "Resource
  exhaustion" below.

### Concurrency issues

**Threat:** concurrent `Get`/`Observe` calls — across the same actor's
key or across many distinct actors' keys — race with each other and
corrupt shared `Baseline` state (a lost update, a torn read, or a data
race that only a `-race` build would catch).

**Status: implemented, and verified under `-race`.** The mechanism this
is safe by construction, not by luck, is `internal/baseline`'s
immutability: `Baseline.Observe` never mutates its receiver — it always
returns a brand-new `Baseline` value with its own `Fingerprints` map
(`internal/baseline/baseline.go`). That means a `Baseline` value a
caller already holds (e.g. from an earlier `Store.Get`) is a permanently
valid snapshot; nothing can retroactively change it out from under a
reader. `internal/store`'s sharded-lock design is the concurrency-safety
layer built on top of that immutability: it serializes the
read-modify-write around each key's `Observe` (so concurrent writers to
the *same* key don't lose an update) while letting writes to *distinct*
keys proceed independently (no unnecessary cross-actor lock contention).
This is verified directly, under `go test -race`, by:

- `TestInMemoryObserveConcurrentSameKey` and
  `TestFileStoreObserveConcurrentSameKey`
  ([`internal/store/store_test.go`](../internal/store/store_test.go),
  [`internal/store/file_test.go`](../internal/store/file_test.go)) —
  many goroutines call `Observe` concurrently against the *same* key and
  assert the final `Count` equals exactly the number of calls made, with
  no lost update.
- `TestInMemoryObserveConcurrentDistinctKeys` and
  `TestFileStoreObserveConcurrentDistinctKeys` (same files) — many
  goroutines call `Observe` concurrently, each against its *own* key,
  and assert every key ends up with its own independent, correct
  `Count`, proving concurrent writes to different actors never
  interfere with each other.

### Resource exhaustion

**Threat:** an attacker (or a misbehaving legitimate producer) sends
input designed to consume disproportionate CPU or memory relative to
its size — an unbounded `Attributes` map, or an actor generating an
unbounded number of distinct fingerprints to grow `Baseline` without
limit.

**Status: bounded.** Fingerprint cardinality per actor is capped at 512
by `internal/baseline`'s `maxFingerprints`, enforced in
`Baseline.Observe` by refusing admission of a new fingerprint once the
cap is reached. `Fingerprint.ID` is derived from `Event` fields the
caller supplies, so this is an untrusted-input dimension; the cap makes
it a bounded one. See
[ADR 0019](adr/0019-bounded-fingerprint-admission.md).

The bound is admission control, not eviction. Nothing already learned is
ever removed to make room, and that asymmetry is the security property:
a fingerprint absent from `Baseline.Fingerprints` scores as maximally
novel with `Confidence = 0`, and `trust.Compute` multiplies anomaly by
confidence — so evicting learned entries under pressure would let a
flood of manufactured fingerprints *suppress* detection for an actor
rather than merely cost memory. Refusing admission preserves everything
the actor has actually been observed doing.

Evidence, asserting the bound rather than the absence of a panic:

| Test | Proves |
|---|---|
| `TestAdversarialStreamStaysBounded`, `TestRejectsOneBeyondCapacity`, `TestAdmitsUpToCapacity` ([`internal/baseline/admission_test.go`](../internal/baseline/admission_test.go)) | 5,000 distinct fingerprints leave exactly 512; the 513th is refused; nothing below the cap is |
| `TestKnownFingerprintKeepsLearningAtCapacity` (same file) | A full baseline is not a frozen one — known fingerprints keep accumulating evidence |
| `TestRejectionDoesNotMoveTheHistoryWindow` (same file) | A refused fingerprint cannot re-enter through the predecessor path |
| `TestFingerprintFloodStaysBoundedEndToEnd` ([`engine_test.go`](../engine_test.go)) | The bound holds through the real gated `Analyze`/`Observe` loop, and the pipeline keeps deciding normally while admission is refused |
| `TestInMemoryStoreStaysBoundedUnderFingerprintFlood`, `TestFileStoreRoundTripsOversizedLegacyBaseline` ([`internal/store/admission_persistence_test.go`](../internal/store/admission_persistence_test.go)) | The bound survives persistence, and a pre-`v1.0` baseline holding more than 512 is loaded whole rather than truncated |

`TestAnalyzeLargeAttributesMapDoesNotPanic` in
[`engine_test.go`](../engine_test.go) still covers the other half of
this threat: a 100,000-key `Attributes` map does not panic or error
`Engine.Analyze` (only `duration_ms`/`error` are ever read out of it,
so per-event cost is proportional to what is consumed, not to the map's
size). **There is still no per-event size limit on `Attributes`**, and
that remains a deliberate open decision rather than a shipped control.

`BenchmarkInMemoryMemoryGrowth` measures `store.InMemory.Observe`
against stores pre-populated with 100, 1,000, and 10,000 distinct keys;
per-call cost is flat (464 B, 3 allocs at every key count), so an
attacker cannot degrade per-event throughput by inflating the key
space. See
[PERFORMANCE.md § measured results](PERFORMANCE.md#measured-results).

**What remains unbounded, stated plainly:** the number of distinct
*actors* a store holds. One entry per `{Scope, ActorID, Environment}`, with no
TTL and no eviction — a deliberate property, since evicting an actor's
baseline silently resets it to "never seen". That is a capacity-planning
dimension owned by whoever provisions the deployment, not a per-request
denial-of-service one, and behavioral-state lifecycle is tracked as
separate work rather than solved here.

`v0.6`'s new `FingerprintStats.PredecessorCounts` (see [Sequence
state](#sequence-state) above) is a deliberate exception to "unbounded
by design" above: unlike `Baseline.Fingerprints` itself, it *is*
capped (64 distinct entries), specifically because it compounds the
existing unbounded-map characteristic with a second, per-entry
dimension an attacker could otherwise inflate independently by varying
the *previous* action on every call.

### Future multi-tenant isolation

**Threat:** in a multi-tenant deployment, one tenant's behavioral data
or policy decisions leak into or influence another's.

**Status: not implemented; the data model is prepared for it.**
Trustvian's OSS core is explicitly single-tenant (multi-tenancy, RBAC,
and centralized management are Trustvian Control/Cloud concerns — see
[ARCHITECTURE.md § relationship to the platform layer](ARCHITECTURE.md#relationship-to-the-platform-layer)).
However, `baseline.Key`'s composite `(Scope, ActorID, Environment)` shape
means the data is already scoped in a way a future `TenantID` addition
extends rather than restructures — a deliberate choice to make that
future work an access-control addition, not a data migration. A learning
scope is **not** a tenant boundary and must not be used as one: it carries
no authorization, and nothing prevents a caller that can reach the engine
from naming any scope. It partitions learning, not access.

### Alert/notification delivery integrity

**Threat:** once a `Decision` can produce an externally-delivered
`Alert` (webhook, chat, paging system — see
[`docs/archive/project-spec.md` § 18](archive/project-spec.md#18-alert--notification-system)),
a forged, replayed, or tampered delivery could make an external system
act on a notification Trustvian never actually sent, or fail to notice
a real one was dropped.

**Status: implemented for the one delivery mechanism this stage ships
(`alert.WebhookSink`, [task
018](archive/tasks/v0.4/018-alert-notification-foundation.md)).** `NewWebhookSink`
fails closed at construction — not silently at the first `Send` — for
a non-HTTPS destination (`ErrNonHTTPSDestination`,
`TestNewWebhookSinkRejectsNonHTTPS`), a literal loopback/link-local
destination unless explicitly overridden
(`ErrLoopbackDestination`/`WithAllowLoopback`,
`TestNewWebhookSinkRejectsLoopbackDestination`), or a missing signing
secret (`ErrMissingSecret`). Every delivery is signed with
HMAC-SHA256 computed over `"<unix-timestamp>.<payload-body>"`, not the
body alone — binding the timestamp into the signed content is what lets
a receiver enforce a replay window without an attacker being able to
attach a fresh timestamp to a previously-valid signature — verified by
`TestSendSignsPayloadCorrectly` (an independently recomputed HMAC
matches the `X-Trustvian-Signature` header exactly) and
`TestSendTamperedPayloadFailsVerification` (flipping one payload byte
changes the recomputed signature). `TestSendDoesNotLeakSecret` proves
the raw signing secret never appears in the outbound body or any
header. A bounded request timeout (`TestSendRespectsTimeout`) and a
bounded payload size, `ErrPayloadTooLarge`
(`TestSendPayloadTooLargeMakesNoNetworkCall` — the oversized-payload
case makes zero network calls, not merely returns an error after
sending) close the resource-exhaustion angle a webhook to an
operator-configured, potentially attacker-influenced destination would
otherwise open. See [DOMAIN.md § Alert](DOMAIN.md#alert) for the domain
model and [ADR 0007](adr/0007-alert-package-is-public.md) for why
`alert` is a public package.

**What remains deliberately unimplemented:** delivery retry, a
delivery-state/dead-letter mechanism, and deduplication are the
separately-scoped Reliability stage's job (see
[CHANGELOG.md § v0.4.0](../CHANGELOG.md#v040--alert--notification-foundation)), not this one's — a
failed or dropped delivery today simply returns an error to the caller,
with no automatic recovery. Full SSRF protection (DNS-resolution-based
destination validation, not just literal-IP loopback checks) is
explicitly out of scope for the same reason `docs/ARCHITECTURE.md`
already draws this boundary for the rest of Trustvian: Trustvian is not
itself a network-egress enforcement point, and real SSRF protection
belongs at the deploying application's network layer.

### AI Agent behavioral security

`v0.7` ([task 014](archive/tasks/v0.7/014-ai-agent.md), [ADR
0014](adr/0014-ai-agents-as-first-class-behavioral-actors.md))
introduces no new state, no new pipeline stage, and no agent-specific
detector — an AI agent is `Actor{Type: ActorTypeAIAgent}`, scored by
the identical engine every other actor already uses. The threats below
are organized around that fact: most are already covered by mechanisms
this document already describes for actors in general, applied here to
the AI-agent case specifically; a few are explicitly future work.

- **High-cardinality session IDs (baseline/fingerprint exhaustion).**
  **Status: implemented.** `Context.SessionID` never enters
  `features.StableFeatures`, `Fingerprint`, or `baseline.Key` — an
  attacker (or a legitimately chatty agent) generating an unbounded
  number of distinct session IDs cannot create a corresponding number
  of distinct behavioral identities or baselines this way, because
  session identity and behavioral identity are structurally different
  dimensions. Proven at scale, not just by omission:
  `TestAnalyzeAgentSessionIDDoesNotExplodeBaseline` runs 1,000 events
  with 1,000 distinct `SessionID` values and confirms exactly one
  `Fingerprint` entry accumulates all 1,000 observations.
- **Prompt/tool-argument data leakage into behavioral state.**
  **Status: implemented, by construction.** Trustvian's `Event` model
  has no field for raw prompt text, completion text, or tool argument
  values, and this task adds none. Tool identity
  (`Operation.Name`/`Target.Name`) is documented as required to be a
  stable, low-cardinality string — never argument content — the
  identical "no raw payload in fingerprint identity" principle this
  document already applies to `Attributes` generally (see [Resource
  exhaustion](#resource-exhaustion) above), restated explicitly for
  AI-agent producers, who are the party most likely to have
  prompt/argument data on hand to (mis)use this way. There is no
  mechanism in this module that could retain such data even if a
  careless producer put it in `Target.Name` — it would simply become
  (and stay) part of that Fingerprint's identity, a data-hygiene
  problem for the producer to avoid, not something this module
  redacts after the fact.
- **Agent identity spoofing / identity mismatch.** **Status: same
  boundary as every other actor, not agent-specific.** `Actor.ID` +
  `IdentityConfidence` are inputs Trustvian trusts, not something it
  authenticates (see [`.claude/rules/security.md` § Identity is an
  input, not a computation](../.claude/rules/security.md)) — an AI
  agent is no different from a service or user actor in this respect.
  Verifying the calling agent's real identity is the deploying
  application's authentication layer's job, upstream of Trustvian. A
  future generalization of this boundary — consuming, not verifying,
  identity/delegation/approval confidence uniformly — is planned, not
  scoped: see [ROADMAP.md § Runtime Identity &
  Provenance](ROADMAP.md#runtime-identity--provenance).
- **Delegation abuse** (an attacker forging `DelegatedFrom` to make an
  unauthorized action appear delegated from a trusted agent, or to
  behaviorally "normalize" a forged delegator through repetition).
  **Status: `DelegatedFrom` now has a real consumer ([task
  031](archive/tasks/v0.7/031-delegation-behavioral-semantics.md), [ADR
  0016](adr/0016-delegation-as-behavioral-evidence-not-provenance.md))
  — behavioral novelty is detected, but provenance verification
  remains genuinely future work, honestly labeled, not something this
  task claims to have solved.** `internal/anomaly`'s new
  `delegation_deviation` signal flags a delegator this actor has never
  (or rarely, at this bounded scale) received delegation from — proven
  by `TestAnalyzeDelegationNoveltyDetectedByExistingSignal`. This is
  behavioral evidence only: **a familiar delegator is not thereby
  authorized, and an unfamiliar one is not thereby malicious.** A
  malicious actor that sets `DelegatedFrom = "trusted-agent-A"` and
  repeats an eligible (non-blocked) action enough times will make that
  claim read as behaviorally "familiar" — Trustvian has no mechanism to
  know the claim is false, because nothing in this task (or task 014)
  authenticates it. `DelegatedFrom` remains exactly as unauthenticated
  and self-reported as before this task; a future task adding
  cryptographic/authenticated provenance verification (signed
  delegation claims, a trusted orchestration layer, identity-provider
  evidence) is distinct, larger, out-of-scope future work — see ADR
  0016's own "Future trusted provenance" section and [ROADMAP.md §
  Runtime Identity & Provenance](ROADMAP.md#runtime-identity--provenance)
  for the roadmap-level direction (planned, not yet scoped to a task).
- **Approval self-assertion** (an agent's own event claiming
  `ApprovalStatus = Approved` and having that trusted merely because
  the event says so). **Status: `ApprovalStatus` now has a real
  consumer ([task 030](archive/tasks/v0.7/030-approval-aware-policy-semantics.md),
  [ADR 0015](adr/0015-approval-as-policy-evidence-not-behavioral-anomaly.md)) —
  the trust boundary below is enforced by construction, not merely
  documented, but provenance verification itself remains future
  work.** `policy.Condition.ApprovalStatus` lets a `Policy` require
  `Approved` for a given operation, via the existing `Unless`
  mechanism — but `ApprovalStatus` is still exactly what it was before
  this task: an unauthenticated, self-reported field. Task 030 adds no
  cryptographic verification, no OAuth/IAM check, and no call to an
  external authorization system — it only makes the *evaluation* of
  that (still-untrusted) evidence deterministic and explainable. Two
  guarantees are enforced, not aspirational, proven by test:
  - **The event cannot define its own requirement.** An event
    self-declaring `ApprovalNotRequired` cannot exempt itself from a
    `Policy` rule that requires `Approved` — only the existence of the
    rule, authored in `Policy`, determines whether approval is
    required at all. Proven by
    `TestEvaluateApprovalPolicyAuthorityEventCannotOverridePolicy`.
  - **Missing evidence fails closed.** `ApprovalUnspecified` (no
    evidence recorded) is treated the same as an explicit `Denied` —
    `BLOCK`, never a silent pass. Proven by
    `TestEvaluateApprovalFailSafeOnMissingEvidence`.

  What remains genuinely future work, same shape as delegation abuse
  above: verifying that a given `ApprovalStatus = Approved` value
  actually originated from a trusted human/authorization system, as
  opposed to the event producer's own unverified claim. An AI agent
  today can still self-assert `Approved` and have `Policy` accept that
  evidence at face value — Trustvian evaluates the evidence it is
  given; it does not (yet, and not as part of task 030) verify where
  that evidence came from. A future task adding provenance
  verification (e.g., a signed assertion from a specific
  authorization system) is a distinct, larger piece of work this task
  deliberately did not build ahead of a concrete need — see
  [ROADMAP.md § Runtime Identity &
  Provenance](ROADMAP.md#runtime-identity--provenance) for the
  roadmap-level direction (planned, not yet scoped to a task).
- **Tool abuse (unexpected/rare tool usage).** **Status: implemented,
  via existing signals.** `categorical_novelty` and
  `transition_deviation`/`transition_rarity` already flag a tool an
  agent has never (or rarely) used — proven for the "never used" case
  by `TestAnalyzeAgentToolNoveltyDetectedByExistingEngine`. No new
  detector was built or is needed.
- **External exfiltration path (a sensitive read followed by an
  outbound call).** **Status: implemented, via existing `v0.6`
  sequence signals — the task's own central proof.**
  `TestAnalyzeAgentToolSequenceNoveltyDetectedByExistingEngine` shows
  `ngram_deviation` (task 027) detecting exactly this shape:
  `search -> secret.read -> external.post` flagged as novel even
  though both individual hops (`search -> secret.read`,
  `secret.read -> external.post`) are independently familiar. Marking
  a specific destination as always-sensitive regardless of
  familiarity is `anomaly.Config.SensitiveTargetFloor`'s existing job
  (see [Malicious agents / privilege
  escalation](#malicious-agents--privilege-escalation) above) —
  unchanged, and already applicable to agent-sourced events with zero
  modification.
- **Baseline poisoning via repeated malicious tool-call sequences.**
  **Status: implemented, inherited.** Every new field this task adds
  is context-only and never mutates learned state on its own; the
  existing `eligibleForLearning` gate ([Baseline
  poisoning](#baseline-poisoning) above) already governs whether *any*
  event — agent-sourced or not — is eligible to update a `Baseline`.
  This task introduces no new learning path and therefore no new way
  to bypass that gate.
- **Rapid tool-call state exhaustion (an agent issuing tool calls far
  faster than a human-driven actor would).** **Status: bounded by
  existing, pre-agent mechanisms.** Every behavioral-state structure a
  rapid-fire agent could grow (`PredecessorCounts`, `TrigramCounts`,
  `TrigramContinuationTotal`) is already independently bounded at 64
  entries (tasks 025/027) regardless of call *rate* — a fast agent
  fills the same bounded structures faster, it does not grow them
  larger. `frequency_deviation` (task 004) is the existing,
  general-purpose signal for anomalously high call rates; no
  agent-specific rate limiting exists or was added.
- **Agent-to-agent graph analytics** (mapping delegation relationships
  across many agents to find escalation or collusion patterns).
  **Status: explicitly future work, not built.** `DelegatedFrom`
  records one hop; no graph, no multi-hop traversal, no relationship
  analytics exists. See [ADR 0014](adr/0014-ai-agents-as-first-class-behavioral-actors.md)'s
  "Delegation: one hop, no graph" section.

### Agent security scenario matrix

[Task 032](archive/tasks/v0.7/032-agent-security-scenario-validation.md) validated
the mechanisms above in composition, against five representative
scenarios plus one combined case — no new detector, no new mechanism.
Each row's "Security limitation" is unchanged from that mechanism's
own entry above; this table only summarizes which existing mechanism
answers which scenario, and is honest about what each does *not*
prove.

| Scenario | Detection/Policy Mechanism | Security Limitation |
|---|---|---|
| Unexpected privileged tool | `categorical_novelty`/`transition_deviation` (existing anomaly) | Behavioral only — a familiar but genuinely malicious tool is not caught by novelty |
| Sensitive read → external post sequence | `ngram_deviation` (bounded 3-gram, task 027) | Process-local, bounded history (64 entries) — not a full audit trail |
| Missing/denied approval | `Policy`'s approval condition (task 030) | Evidence provenance is external; `ApprovalStatus` is unverified, self-reported input |
| Unexpected delegator | `delegation_deviation` (task 031) | Provenance is not authenticated; familiar does not mean authorized, novel does not mean malicious |
| External destination drift | `categorical_novelty` via `Target.Category` | Depends entirely on the producer supplying normalized, truthful target metadata |
| Combined (delegation + sequence + approval) | all of the above, composed via noisy-OR + `Policy` | Same limitations as each row above, individually — composition adds no new guarantee beyond them |

Task 032 found, and [task
033](archive/tasks/v0.7/033-v07-stabilization-release-gate.md) closed, a public-API
gap: `anomaly.Config` had no `config`-package equivalent of
`policy.Policy`'s `config.CompilePolicy` path, so an OSS consumer
outside this module could not enable the `delegation_deviation`/`v0.6`
sequence signals through public API alone. `config.AnomalyConfig`/
`CompileAnomaly` (see the "Anomaly configuration-input validation"
section above and [ADR
0017](adr/0017-public-anomaly-configuration-boundary.md)) now closes
that gap — every row in the table above, including the combined
scenario, is demonstrable through public API alone. See
[examples/ai-agent-security](../examples/ai-agent-security/)'s own
README for the worked example.

### Runtime health endpoints

**Threat:** an operational endpoint becomes an information leak or a false
signal — a probe response that reveals the DSN, database host, SQL errors,
or behavioral data to anyone who can reach the port; or a liveness check
that fails whenever PostgreSQL does, so a supervisor restarts healthy
processes in a loop and the outage is masked as a crash.

**Status: implemented** ([task
042](archive/tasks/v0.9/042-runtime-health-readiness-graceful-shutdown.md)).

- **Near-zero information content.** `/livez` and `/readyz` return a status
  string and nothing else — no DSN, hostname, error text, configuration, or
  actor data. The endpoints are unauthenticated by design, so network
  placement is the access control: the listener is opt-in (no `health:`
  block, no listener) and the reference deployment binds it to loopback.
  Why a probe failed goes to the runtime's logs, not the response.
  `TestHandlerLeaksNothing`, `TestNoHealthConfigServesNothing`.
- **Liveness never consults the store.** A PostgreSQL outage makes the
  runtime not ready, never not live. `TestFailingProbeDoesNotAffectLiveness`.
- **Readiness never implies a fallback.** PostgreSQL configured and
  unusable reports not ready; the store is never substituted.
  `TestPingReportsDatabaseUsability` against a real server, and the recovery
  drill's outage step on the running Collector.
- **Bounded.** A wedged database cannot hold a probe open past its
  timeout, and the listener has a header-read timeout.
  `TestReadyIsBoundedByProbeTimeout`.

### Operational metrics privacy

**Threat:** self-observability exports what Trustvian was built to keep
bounded — actor identities, trace IDs, raw error text (which can contain
SQL or connection details), or behavioral payload as metric attributes —
into a telemetry backend with different access controls; or an unbounded
label set turns the metrics pipeline into a resource-exhaustion vector
driven by attacker-chosen actor IDs.

**Status: implemented** ([task
043](archive/tasks/v0.9/043-self-observability-resource-safety.md)).

- **Closed vocabularies only.** Two attribute keys exist
  (`trustvian.outcome`, `trustvian.decision`), each with a fixed value set;
  an unrecognized decision records as `other`. Total cardinality is 15 time
  series regardless of traffic. `TestNoForbiddenAttributes`,
  `TestUnknownDecisionIsFolded`, `TestObserveErrorRecordsBoundedCategory`.
- **No subject data.** Actor IDs, trace IDs, raw errors, DSNs, and
  behavioral payload cannot become attributes — asserted against recorded
  telemetry, not documentation.
- **One-way.** Operational metrics are recorded into the Collector's
  `MeterProvider` and never read back into an `Engine`; exporter failure
  cannot change a decision. See [observability.md](observability.md).

### Backup and restore

**Threat:** learned behavioral state is lost, disclosed, or silently reset
through the operational paths around it rather than through the engine —
a backup leaks credentials or behavioral profiles, a corrupt backup is
restored as if it were good, a restore overwrites the live database, or a
failed restore leaves an empty database that Trustvian adopts as a new
deployment and starts learning from zero. Every one of these ends in the
same place as baseline poisoning's worst case: actors Trustvian had learned
become unfamiliar, and nothing reports an error.

**Status: implemented** ([task
044](archive/tasks/v0.9/044-operations-backup-restore-upgrade.md)). The operator
procedure is [`operations.md`](operations.md); this section records the
security properties it depends on.

| Property | Mechanism | Evidence |
|---|---|---|
| Backups are confidential by default | Artifacts created under `umask 077`: directory `0700`, files `0600` | `TestBackupRestorePreservesLearnedBehavior` asserts modes |
| No credentials in backups or their output | Connection only via the libpq environment — no DSN flag, so nothing reaches a process listing; `MANIFEST` holds no host, user, database, DSN, or password; libpq errors never print passwords | Manifest values compared against every connection parameter; outputs checked for a distinctive password, including on an unreachable-server failure |
| Corruption is detected before restore | SHA-256 over dump **and** manifest; a checksum file not covering exactly both is refused; no bypass flag | `TestRestoreScriptRefusesUnverifiableBackups` |
| The live database is never overwritten | Restore requires an existing, **empty** target with no other sessions, never the `PGDATABASE` database; refusals write nothing | Non-empty and in-use targets refused and verified untouched |
| A failed restore is never adopted | `--single-transaction` (nothing half-restored), then the target is quarantined with `ALLOW_CONNECTIONS false` so a runtime pointed at it fails to start instead of silently initializing an empty schema | Truncated-archive test asserts zero relations and a refused Trustvian startup; mutation removing `--single-transaction` or the quarantine fails it |
| One schema authority | Restore verifies structure only; compatibility is decided by Trustvian's own `Migrate` at startup, fail-closed | Newer-schema restore refused with `ErrSchemaVersionMismatch` |
| No automatic cutover | Scripts never create, drop, or rename databases, and never repoint a runtime | — |
| Upgrades cannot silently reinterpret state | Schema-version mismatch fails startup in both directions; binary-only downgrade across a schema change is refused | Upgrade test from the real `v0.8.0` release |

Two limits stated plainly:

- **A checksum is integrity, not authenticity.** Whoever can modify a backup
  can regenerate `SHA256SUMS`. Tamper evidence requires storing the checksum
  file where the backup's writer cannot change it, or signing it.
- **A backup is a copy of every actor's behavioral profile.** Encryption at
  rest, access control, and separation from the primary database are the
  operator's responsibility; Trustvian ships no storage for backups.

A related contract for future releases: **any change to the persisted
`Baseline` shape must bump the schema version.** Otherwise a binary-only
downgrade would decode the newer rows while ignoring unknown fields and
silently drop them on its next write — exactly the silent reinterpretation
the version check exists to prevent.

## Software supply-chain security

Distinct from everything above. The rest of this document is about
Trustvian's **runtime** security — how it evaluates behavior and resists
manipulation. This section is about the integrity of the **artifacts** you
install: which commit produced them, what is inside them, and how you
confirm both.

It is also unrelated to the separately planned *Runtime Identity &
Provenance* capability, which concerns the provenance of observed actors.
Same word, different layer.

| Property | Mechanism |
|---|---|
| Artifact matches the release source | The release workflow validates the tag as SemVer, verifies the checked-out commit equals the tagged commit, and builds binaries and the container image from that commit |
| Release gates cannot be bypassed | Gates re-run against the tagged source rather than trusting an earlier workflow run on a different commit |
| Binary integrity | SHA-256 checksum manifest published with every release |
| Artifact traceability | `trustvian version` reports the module version, commit revision, and whether the build tree was modified, read from Go's own build information |
| Container contents are known | SPDX SBOM attached to the image as an attestation |
| Container origin is verifiable | SLSA provenance attestation, plus keyless Cosign signature over the image digest |
| No key material to compromise | Signing uses GitHub OIDC (Sigstore keyless); no signing key exists in the repository or in a secret |
| No long-lived registry credential | Publishing authenticates to GHCR with the workflow-scoped token |
| Known vulnerabilities are gated | `govulncheck` fails on any *reachable* Go vulnerability; Trivy fails on fixable `CRITICAL`/`HIGH` in the image |
| Least privilege | Normal CI and nightly workflows are `contents: read`. `packages: write` and `id-token: write` exist only in the container-publishing job. No `pull_request_target` anywhere |
| Minimal runtime surface | The image runs non-root with no shell, no package manager, and no added capabilities |

Two limits stated plainly:

- **The container scan gates on a `linux/amd64` build** whose cache the
  multi-arch push reuses. The arm64 layer is built from the same source and
  the same base version but is not itself scanned before publishing.
- **Byte-for-byte reproducibility is not claimed** for either binaries or
  images. The builds avoid obvious nondeterminism, but this is untested, and
  an untested reproducibility claim is worse than none.

Full detail, including the vulnerability policy and its current exceptions:
[`supply-chain.md`](supply-chain.md).

## Explainability as a security property

Every `Anomaly` retains its `Contributors`; every `policy.Result`
carries a non-empty `Explanation`. This isn't incidental — an
un-explainable decision is itself a kind of risk (an operator can't
audit or contest what they can't see the reasoning for). These
properties are checked by tests
(`TestEvaluateAlwaysProducesNonEmptyExplanationReason`), not just
documented as an aspiration.

## What Trustvian does not protect against

- Compromise of the process it runs in (memory tampering, a malicious
  binary). Out of scope for an in-process library.
- Weaknesses in the identity/authentication system upstream of it — it
  consumes `IdentityConfidence` as an input, it does not produce it.
- Network-level attacks against however events are transported to it —
  that's the transport/collector's responsibility.
