# CI helper maintenance contract

`bash scripts/ci-helper_test.sh` is the deterministic local regression harness
for `scripts/ci-format.sh` and `scripts/ci-test.sh`. It uses fake `git`,
`gofmt`, `go`, and `tee` commands, so it does not require a GitHub Actions
runner or a long real-Git test suite.

The workflow pipes `git ls-files -z -- '*.go'` into `ci-format.sh --stdin`
under `pipefail`; the helper also supports direct local invocation, where it
obtains the same exact inventory itself. Do not replace that NUL-delimited interface with a
glob, a package-directory scan, or a newline-delimited loop: tracked names may
contain spaces or newlines. A Git or formatter failure is fatal, and any
unformatted tracked file is printed and makes the check fail.

`ci-test.sh normal` and `ci-test.sh race` dynamically obtain `go list ./...`.
They run all non-service packages once and obtain the top-level `Test`,
`Example`, and `Fuzz` targets from `go test -list` for the service package.
Targets are assigned round-robin to the configured shard count (default eight),
then validated as disjoint and exhaustive before any service shard starts.
Never maintain a hand-written service-test list. Inventory errors, empty
inventories, duplicates, missing service packages, invalid modes, and any
exact-once violation must fail closed.

The normal and race timeouts are explicit Go-test timeouts: 30 minutes and 45
minutes by default. They leave room for the repository's real-Git service
fixtures while ensuring a hung shard eventually produces a bounded diagnostic.
Use `CI_TEST_NORMAL_TIMEOUT`, `CI_TEST_RACE_TIMEOUT`, and `CI_TEST_SHARDS` only
for controlled local/harness experiments; any workflow timeout change requires
remeasuring the complete matrix and updating this rationale and its tests.

The runner finishes every non-service command and every non-empty service shard
even after a failure, then returns failure. It records the test command and
log-transport (`tee`) statuses independently, emits bounded GitHub annotations
with escaped message/property metacharacters, and extracts timeout, panic,
race, compile, ordinary-failure, or fallback log context. Its owned `mktemp`
directory is removed by traps on success, failure, and interruption. Keep that
cleanup scoped to the helper-owned temporary directory; do not introduce broad
or pathname-derived deletion.

The deterministic harness verifies inventory growth, fewer targets than shards,
exact-once assignment, failures, annotations, log transport, and cleanup. It
is not native Windows runtime evidence: hosted Windows CI and M03 remain the
runtime acceptance path.
