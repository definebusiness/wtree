#!/usr/bin/env bash
# Deterministic contract test for local test-lane composition. It uses make -n
# so it never starts the expensive inventories it inspects.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
require() { [[ $1 == *"$2"* ]] || fail "missing [$2]"; }
reject() { [[ $1 != *"$2"* ]] || fail "unexpected [$2]"; }
capture() { make -n "$@"; }

fast=$(capture check-local)
require "$fast" 'go test -short -count=1 -timeout=90s ./...'
require "$fast" 'local-integration-smoke'
require "$fast" 'go test ./tools/test-runner -count=1'
require "$fast" 'local-test-targets-test'
require "$fast" 'go build -o ./bin/wtree ./cmd/wtree'
reject "$fast" 'test-full-race mode=race'

changed=$(capture test-changed BASE_REF=HEAD)
require "$changed" 'test-runner changed-run --base "HEAD"'
require "$changed" 'changed-run --base "HEAD" --timeout=5m'

changed_race=$(capture test-changed-race "PACKAGES=./internal/testutil ./tools/test-runner")
require "$changed_race" 'go test -short=false -race -count=1 -timeout=5m ./internal/testutil ./tools/test-runner'

full=$(capture test-full TEST_JOBS=1)
require "$full" "printf 'test-full mode=normal workers=%s timeout=%s\\n' \"1\" \"30m\""
require "$full" 'go run ./tools/test-runner run --mode normal --workers=1 --timeout=30m'
full_override=$(capture test-full TEST_NORMAL_TIMEOUT=17m)
require "$full_override" 'go run ./tools/test-runner run --mode normal --workers=4 --timeout=17m'
full_default=$(capture test-full)
require "$full_default" 'go run ./tools/test-runner run --mode normal --workers=4 --timeout=30m'
race=$(capture test-full-race TEST_JOBS=1)
require "$race" "printf 'test-full-race mode=race workers=%s timeout=%s\\n' \"1\" \"45m\""
require "$race" 'go run ./tools/test-runner run --mode race --workers=1 --timeout=45m'
race_override=$(capture test-full-race TEST_RACE_TIMEOUT=47m)
require "$race_override" 'go run ./tools/test-runner run --mode race --workers=4 --timeout=47m'
race_default=$(capture test-full-race)
require "$race_default" 'go run ./tools/test-runner run --mode race --workers=4 --timeout=45m'
if go run ./tools/test-runner run --timeout=0 >/dev/null 2>&1; then
  fail 'runner accepted an invalid zero timeout'
fi

alias_normal=$(capture test)
require "$alias_normal" 'go run ./tools/test-runner run --mode normal'
alias_race=$(capture test-race)
require "$alias_race" 'go run ./tools/test-runner run --mode race'
alias_check=$(capture check)
require "$alias_check" 'go run ./tools/test-runner run --mode normal'
require "$alias_check" 'go run ./tools/test-runner run --mode race'
require "$alias_check" './tutorial/run-all-commands.sh'
require "$alias_check" './scripts/release-build_test.sh'

smoke=$(capture local-integration-smoke)
require "$smoke" '-short=false'

# Exercise actual Make fatal propagation with a bounded fake go command. This
# is intentionally not a dry-run assertion: a failed child must fail the lane.
fake_dir=$(mktemp -d "${TMPDIR:-/tmp}/wtree-local-targets.XXXXXX")
trap 'rm -rf -- "$fake_dir"' EXIT
cat >"$fake_dir/go" <<'EOF'
#!/usr/bin/env bash
exit 47
EOF
chmod +x "$fake_dir/go"
if PATH="$fake_dir:$PATH" make test-changed-race PACKAGES='./internal/testutil' >/dev/null 2>&1; then
  fail 'test-changed-race masked a child go failure'
fi

# GOFLAGS may be ambient in a developer shell. The non-short smoke contract
# must explicitly override it; otherwise these classified fixture tests skip.
if ! GOFLAGS=-short make local-integration-smoke >/dev/null 2>&1; then
  fail 'ambient GOFLAGS=-short narrowed the non-short smoke lane'
fi

# Recipes deliberately use make's default fatal error propagation: no test
# lane may mask a child test failure with `|| true`, a trailing `; true`, or a
# pipe whose exit status would replace the child status.
makefile=$(<Makefile)
reject "$makefile" '|| true'
reject "$makefile" '; true'

printf 'local test target contract passed\n'
