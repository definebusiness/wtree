.PHONY: test test-race test-full test-full-race test-changed test-changed-race check-local check-full local-test-targets-test local-integration-smoke vet build release release-test tutorial-test lifecycle-hook-tutorial-test check fmt-check

# TEST_TIMEOUT remains a compatibility override for callers that intentionally
# want one bound for both test modes. The mode-specific defaults match CI's
# authoritative complete-suite bounds.
TEST_TIMEOUT ?=
TEST_NORMAL_TIMEOUT ?= $(if $(TEST_TIMEOUT),$(TEST_TIMEOUT),30m)
TEST_RACE_TIMEOUT ?= $(if $(TEST_TIMEOUT),$(TEST_TIMEOUT),45m)
TEST_SHORT_TIMEOUT ?= 90s
TEST_CHANGED_TIMEOUT ?= 5m
TEST_SMOKE_TIMEOUT ?= 2m
TEST_JOBS ?= 4

# test and test-race remain exhaustive compatibility aliases.  They must not
# be repointed at check-local: existing plans rely on complete evidence.
test: test-full

test-race: test-full-race

test-full:
	@printf 'test-full mode=normal workers=%s timeout=%s\n' "$(TEST_JOBS)" "$(TEST_NORMAL_TIMEOUT)"
	go run ./tools/test-runner run --mode normal --workers=$(TEST_JOBS) --timeout=$(TEST_NORMAL_TIMEOUT)

test-full-race:
	@printf 'test-full-race mode=race workers=%s timeout=%s\n' "$(TEST_JOBS)" "$(TEST_RACE_TIMEOUT)"
	go run ./tools/test-runner run --mode race --workers=$(TEST_JOBS) --timeout=$(TEST_RACE_TIMEOUT)

# BASE_REF is deliberately mandatory. The runner includes committed, staged,
# unstaged, and untracked paths and fails closed on ambiguous ownership.
test-changed:
	@test -n "$(BASE_REF)" || (printf '%s\n' 'BASE_REF=<commit> is required' >&2; exit 2)
	@printf 'test-changed mode=normal base=%s timeout=%s\n' "$(BASE_REF)" "$(TEST_CHANGED_TIMEOUT)"
	go run ./tools/test-runner changed-run --base "$(BASE_REF)" --timeout=$(TEST_CHANGED_TIMEOUT)

# Race selection is explicit because a path-name heuristic cannot establish
# concurrency risk. Pass package patterns as one quoted PACKAGES value.
test-changed-race:
	@test -n "$(PACKAGES)" || (printf '%s\n' "PACKAGES='<race-sensitive package patterns>' is required" >&2; exit 2)
	@printf 'test-changed-race mode=race timeout=%s packages=%s\n' "$(TEST_CHANGED_TIMEOUT)" "$(PACKAGES)"
	go test -short=false -race -count=1 -timeout=$(TEST_CHANGED_TIMEOUT) $(PACKAGES)

local-integration-smoke:
	go test -short=false -count=1 -timeout=$(TEST_SMOKE_TIMEOUT) ./internal/testutil -run '^TestNewGitRepositoryRunsOutsideShortMode$$'
	go test -short=false -count=1 -timeout=$(TEST_SMOKE_TIMEOUT) ./internal/cli -run '^TestEndToEndCompositionAcceptance$$'
	go test -short=false -count=1 -timeout=$(TEST_SMOKE_TIMEOUT) ./internal/service -run '^TestDirectProcess(RunsWithoutShellAndBoundsStreams|CancellationKillsDescendants)$$'

local-test-targets-test:
	bash scripts/local-test-targets_test.sh

check-local: fmt-check vet
	go test -short -count=1 -timeout=$(TEST_SHORT_TIMEOUT) ./...
	$(MAKE) local-integration-smoke
	go test ./tools/test-runner -count=1
	$(MAKE) local-test-targets-test
	$(MAKE) build

vet:
	go vet ./...

build:
	go build -o ./bin/wtree ./cmd/wtree

install:
	go install ./cmd/wtree

# release creates only local artifacts; publishing is intentionally out of scope.
release:
	VERSION=$${VERSION:?set VERSION, for example VERSION=1.2.3} DIST_DIR=$${DIST_DIR:-dist} ./scripts/release-build.sh

release-test:
	./scripts/release-build_test.sh

tutorial-test:
	./tutorial/run-all-commands.sh
	$(MAKE) lifecycle-hook-tutorial-test
	go test ./internal/git ./internal/service -run 'TestRelease(FetchAuthenticationChannelsReachGitAndSecretsDoNotEscape|MaterializeFetchesAdvertisedCommitAndPublishesDetachedChild|MaterializeDryRunNeverContactsUnavailableRevision|MaterializeStagesNestedAndSiblingBeforePublication|MaterializeAuthenticationFailureLeaksNoCanaryToArtifacts|LockCreatesReplacesAndProtectsCandidate|LockPostReleasePreflightAndCoreFailureSemantics)' -count=1
	./tutorial/run-releases.sh

lifecycle-hook-tutorial-test:
	go test ./internal/config ./internal/service ./internal/store ./internal/cli -run 'Test(LifecycleHook(TutorialAcceptance|PublicContractMatrix)|PortableHookCommandSyntaxIsElementAwareAndCrossPlatform|HooksCommandsRenderVersionedResultsAndKeepJSONSeparateOnErrors|CloneV3PortableHooksDryRunAndUnauthorizedSkipPublicContracts|CreateHookRunnerPersistsFirstSuccessAndStopsAtLaterFailure|CreateNoHooksValidatesAndCommitsWithoutHookAuthority|HookRunnerResumesFailedAndFinalizingRecords|HookRunnerSerializesConcurrentSameEvent|HookRetryUsesSingleInventoryCandidateAndRendersBoundedResult|HookEnvironmentPortableAllowlistExcludesSecrets|HookRunRecordRoundTripAndPrivacy|HookProcess(ClassifiesOutputTimeoutCancellationAndNonZero|ForcedBoundaryNeverLeaksCredentialContinuation|ForcedBoundaryRedactsNewlineTerminatedContinuations)|UpdatePublicationPreservesLocalV3HookConsentWithoutExecutingSharedContent)' -count=1

fmt-check:
	@unformatted="$$(go list -f '{{.Dir}}' ./... | while IFS= read -r dir; do gofmt -l "$$dir"/*.go; done)"; \
	test -z "$$unformatted" || (printf '%s\n' "$$unformatted" && exit 1)

check-full: fmt-check vet test-full test-full-race build tutorial-test release-test local-test-targets-test

check: check-full
