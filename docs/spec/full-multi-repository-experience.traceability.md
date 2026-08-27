# Full multi-repository experience P0/P1 traceability

Status: planned
Source idea: none (traceability companion created directly)
Source specification: [Full multi-repository experience capability specification](full-multi-repository-experience.md)
Implementation plan: [Multi-repository composition loop and aggregate operations implementation plan](../plans/full-multi-repository-experience.md)

This companion records the delivered P0/P1 slice only. It does not advance
the source specification beyond `planned`: the P2/P3/P4 capabilities remain
explicitly deferred and require their own specifications and plans.

## Delivered P0/P1 requirements

All command tests below execute through the public `cli.Execute` boundary
unless they name a service or Git adapter test. The paired
`TestEndToEndCompositionAcceptance` and
`TestEndToEndCompositionAcceptanceMembership` flows are the lean hermetic
nested-forest evidence: their only fixture-owned remote writes seed local
bare repositories; `wtree` is never asked to publish. The CI
workflow runs formatting, vet, normal and race tests, a build, release-layout
and release-directory reuse checks, and manifest-layout checks on Ubuntu,
macOS, and Windows.

| Source contract | Owning delivered milestone | Focused evidence | Integrated/public evidence | CI and safety evidence |
|---|---|---|---|---|
| §3.1 independent repositories; §3.2 IDs/identity; §3.3 portable-data secrecy | M00, M01 | `TestAdapterRedactsRemoteCredentialsAndDoesNotRunHooks`, `TestDriftSnapshotRequiresAuthoritativeConfigPathAndManifestBinding` | `TestEndToEndCompositionAcceptance` resolves the cloned root and child by ID and mount | Strict portable-v2 tests; sanitized adapter environment; no dependency or schema change |
| §3.4–§3.5 immutable preflight, deterministic order, owned rollback/recovery | M00–M04 | `TestUpdateExecutBoundaryMatrixProductionEffectFamilies`, `TestUpdateExecutRollbackIsReverseAndJournalIsRetainedOnlyForIncompleteUndo` | `TestEndToEndCompositionAcceptance` runs update dry-run then update on the default workspace | normal/race CI; journal/recovery tests preserve exact owned authorities |
| §3.6 branch and selected-ref safety; §3.8 parent-first forest order | M00–M04 | `TestFetchConfiguredRefObservesAndFetchesOnlyTheConfiguredRef`, `TestUpdateExecutProductionConfiguredFetchFailuresRestoreExactTrackingGeneration` | nested root/backend clone, update, exec, and fetch acceptance flow | no fallback branch/ref behavior; Git adapter tests cover hooks and hostile configuration |
| §3.7 stable JSON and dry-run; §4 aggregate result/error contract | M00–M02 | `TestAggregateFailureRedactsAndBoundsDiagnostics`, `TestExecuteUpdateDryRunRendersStableJSONWithoutMutation`, `TestUpdateCommandProductionSourceAuthorityAndRedaction` | acceptance flow validates decoded JSON for clone, update, doctor, status, exec, fetch, and push | stdout writer/partial-writer, cancellation, deterministic order, and redaction tests run under normal/race CI |
| §5.1 update classification, source precedence, transactional publication, rollback, reconciliation | M01–M04 | `TestUpdateSnapshotCollectorAncestryErrorsAndCancellationRemainObservationBoundaries`, `TestUpdateExecutAddedRepositoryStagesVerifiesPublishesAndOnlyRemovesOwnedReceipt`, `TestUpdateExecutRecoveryReopensStrictJournalAndRestoresExactTrackedManifest` | acceptance flow proves clone-derived default workspace accepts update dry-run and completed update before later operations | version-1 journal/reconciliation is strict and private; no existing schema is extended |
| §5.1 added and retained removal semantics | M01–M04 | `TestUpdateExecutAddedRepositoryStagesVerifiesPublishesAndOnlyRemovesOwnedReceipt`, `TestUpdateExecutPreparesPrivateRetainedFactsWithoutTouchingCheckout` | membership acceptance publishes a tracked candidate adding `extra`, resolves default and named workspaces, then removes `backend` while retaining its checkout and decoding reconciliation | update never deletes retained repositories; rollback/recovery uses owned evidence only |
| §5.2 init/clone portable acquisition baseline | pre-existing contracts, consumed by M00–M04 | `TestClonePlanLocalAndHTTPManifestSourcesYieldEquivalentValidatedPlan`, `TestCloneExecuteRejectsByteDifferentServedV2ManifestAndCleansUp` | acceptance flow performs clone dry-run and a real portable v2 nested-forest clone | clone v2/local config v2 are unchanged; no ambient user-data access |
| §5.3 doctor drift and recovery visibility | M05 | `TestDoctorCollectionWrapsAndRedactsBaseIdentityError`, `TestEndToEndDoctorSurfacesRecoveryRecordWithoutMutatingState` | acceptance flow invokes decoded doctor output before aggregate operations | existing allowlisted `doctor --fix` remains narrow; no inferred clone/move/delete/fast-forward |
| §6.1 direct `exec`, order, environment, cancellation, bounded output | M00, M06 | `TestExecContinuesAfterNonZeroExit`, `TestExecBoundsAndRedactsBothStreamsAtTheCommandBoundary`, `TestExecuteExecJSONExactSchemasAndApplicationCategories` | acceptance flow directly invokes `git rev-parse` across root and child | no implicit shell; explicit direct argv only; cancellation and partial-writer evidence is focused |
| §6.2 explicit `fetch` and network-free drift-aware `status` | M00, M07, M08 | `TestFetchContinuesAfterOrdinaryConfiguredRefFailuresAndRedacts`, `TestStatusWithDataDirUsesTrackedManifestWithoutRemoteOrMutation`, `TestExecuteStatusRendersTrackedManifestAbsentAndReplacementDrift` | acceptance flow advances only a local fixture remote, fetches the configured ref, then compares two byte-identical local status documents | fetch is deliberately non-transactional; status does not contact remotes or mutate |
| §6.3 non-publishing `push` readiness | M09 | `TestPushCapturesOneManifestAuthorizedUpstreamBeforeRemoteObservation`, `TestAdapterRedactsRemoteCredentialsAndDoesNotRunHooks`, `TestExecutePushBlockedHumanAndJSONHaveOneDeterministicDocument` | acceptance flow snapshots local refs before/after push readiness and accepts its ready or blocked decoded document | no `git push`, tag, branch, state, lock, or recovery write; cancellation/writer/redaction have focused coverage |
| Error taxonomy, strict v1 command envelopes, recovery visibility, help/how-to | M00–M09 | `TestExitCodeMapsEveryApplicationCategory`, `TestRawCLIErrorHasMatchingJSONCodeAndExitCode`, `TestHowToCoversAllTopicsAndCommandGuides` | root help, command help, README, tutorial, and troubleshooting describe update, exec, fetch, status, and push readiness | `go vet`, formatting, normal/race tests, build, and release layout run in CI |

## Combined adverse inventory

The paired acceptance tests deliberately use only compatible local work. They
decode every public aggregate row in parent-first order, prove recursive
resolver-owned checkout/origin/project/data/Git authority equality for
non-mutating rows, and preserve retained reconciliation across later commands.
The focused tests below cover the high-risk combinations that would otherwise
turn the acceptance flow into a slow Cartesian Git matrix:

| Adverse contract | Evidence |
|---|---|
| Aggregate failure, deterministic v1 JSON, cancellation, exact writer error | membership acceptance proves all three current repositories settle failed for one direct-argv error without authority mutation; `TestAggregateFailureRedactsAndBoundsDiagnostics`, `TestExecuteUpdateDryRunRendersStableJSONWithoutMutation`, `TestExecuteFetchMalformedChildWriterErrorIsExact`, `TestPushCancellationAndWriterStopRemoteSuffix` cover the remaining command boundaries |
| Update rollback, rollback-incomplete authority, recovery visibility, post-recovery usability | `TestUpdateExecutProductionRollbackAndRecoveryLeaveSecondPreflightUsable`, `TestUpdateExecutRecoveryRetainsTamperedOrConcurrentEvidence`, `TestEndToEndDoctorSurfacesRecoveryRecordWithoutMutatingState` |
| Dirty, divergent, missing, partial, and retained states | `TestUpdateClassificationRejectsObservedCheckoutDrift` (dirty, divergent/detached, missing), `TestDriftSnapshotRejectsImportedPartialWorkspaceAndRedactsOperationDiagnostics`, `TestDriftSnapshotClassifiesRemovedRetainedInParentFirstOrder`, `TestUpdateExecutPreparesPrivateRetainedFactsWithoutTouchingCheckout` |
| Push finding coverage and non-mutation for every finding | `TestPushEveryFindingPreservesCompleteAuthoritySnapshot`, `TestPushMapsAheadBehindDivergedAndUnpublishedHead`, `TestPushFindingContractAndM09Mappings` |
| No shell, hook, push, tag, branch/ref mutation, credentials, or external network | `TestExecRunsDirectArgvInDeterministicOrder`, `TestAdapterRedactsRemoteCredentialsAndDoesNotRunHooks`, `TestPushDoesNotRunConfiguredFSMonitorHook`, `TestEndToEndCompositionAcceptance` |
| Recursive before/after authority preservation | `TestExecuteUpdateDryRunRendersStableJSONWithoutMutation`, `TestUpdateExecutOpaqueBackupsAreExactPrivateAndTamperEvident`, `TestPushReadyPreservesCompleteAuthoritySnapshot` |

Fixture-owned local commits and pushes only seed `testutil.NewPushedGitRepository`
bare remotes. They never reach an external host, user repository, release,
or publication channel.

## Deferred capabilities and entry gates

| Deferred source area | Not delivered by M00–M10 | Required entry gate before implementation |
|---|---|---|
| P2 / §7.1 release locks | lock schema, immutable release materialization, lock-aware update | focused lock specification and plan defining strict schema, selected revisions, publication, rollback, recovery, and compatibility |
| P2 / §7.2 heterogeneous workspaces | mixed/locked workspace kinds and omission semantics | focused workspace-state specification and plan preserving current partial-import JSON/refusals and adding explicit compatibility tests |
| P3 / §8 migration | Gitlink/submodule/subtree detection and migration | focused migration specification and plan with opt-in behavior, non-destructive preflight, rollback, and current-workspace usability evidence |
| P3 / §8 hooks | trusted hook sources, execution, retries, and hook recovery | focused hook-trust and retry specification and plan; no manifest code execution before it exists |
| P4 / §9 selection | repository subsets and selective materialization | focused selection specification and plan defining partial/omitted semantics, strict JSON, and doctor/status compatibility |
| P4 / §9 transport and URL profiles | transport modes, credential profiles, URL rewriting | focused profile/schema specification and plan defining redaction, secret storage, validation, rollback, and diagnostics |
| §10 compatibility constraints | any version/migration or changed persisted/portable meaning | §10.7 compatibility-resolution gate: identify touched CLI/JSON/state/manifest/registry/Git contracts; declare additive/opt-in/migrated/versioned behavior; regression-test unaffected workflows; and prove migration/rejection, rollback, and post-change usability |

## Audit boundary

The M10 audit found no demonstrated correction needed in the root and command
help, README, installed how-to, tutorial, troubleshooting guide, strict
schema versions, error/exit mapping, redaction, recovery visibility, release
layout, or CI configuration. The tracked workflow already runs the required
Ubuntu/macOS/Windows matrix and its build/release-layout checks. A run for the
uncommitted M10 tree still requires separately authorized external commit or
CI triggering; this document does not claim that external evidence.
