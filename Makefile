.PHONY: test test-race vet build release release-test tutorial-test lifecycle-hook-tutorial-test check fmt-check

# TEST_TIMEOUT remains a compatibility override for callers that intentionally
# want one bound for both test modes. The mode-specific defaults match CI's
# authoritative complete-suite bounds.
TEST_TIMEOUT ?=
TEST_NORMAL_TIMEOUT ?= $(if $(TEST_TIMEOUT),$(TEST_TIMEOUT),30m)
TEST_RACE_TIMEOUT ?= $(if $(TEST_TIMEOUT),$(TEST_TIMEOUT),45m)

test:
	go test -timeout=$(TEST_NORMAL_TIMEOUT) ./...

test-race:
	go test -race -timeout=$(TEST_RACE_TIMEOUT) ./...

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

lifecycle-hook-tutorial-test:
	go test ./internal/config ./internal/service ./internal/store ./internal/cli -run 'Test(LifecycleHook(TutorialAcceptance|PublicContractMatrix)|PortableHookCommandSyntaxIsElementAwareAndCrossPlatform|HooksCommandsRenderVersionedResultsAndKeepJSONSeparateOnErrors|CloneV3PortableHooksDryRunAndUnauthorizedSkipPublicContracts|CreateHookRunnerPersistsFirstSuccessAndStopsAtLaterFailure|CreateNoHooksValidatesAndCommitsWithoutHookAuthority|HookRunnerResumesFailedAndFinalizingRecords|HookRunnerSerializesConcurrentSameEvent|HookRetryUsesSingleInventoryCandidateAndRendersBoundedResult|HookEnvironmentPortableAllowlistExcludesSecrets|HookRunRecordRoundTripAndPrivacy|HookProcess(ClassifiesOutputTimeoutCancellationAndNonZero|ForcedBoundaryNeverLeaksCredentialContinuation|ForcedBoundaryRedactsNewlineTerminatedContinuations)|UpdatePublicationPreservesLocalV3HookConsentWithoutExecutingSharedContent)' -count=1

fmt-check:
	@unformatted="$$(go list -f '{{.Dir}}' ./... | while IFS= read -r dir; do gofmt -l "$$dir"/*.go; done)"; \
	test -z "$$unformatted" || (printf '%s\n' "$$unformatted" && exit 1)

check: fmt-check vet test test-race build tutorial-test
