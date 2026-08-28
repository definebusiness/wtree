#!/usr/bin/env bash
# Deterministic local regression harness for the CI helper scripts.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/wtree-ci-helper-test.XXXXXX")
trap 'rm -rf -- "$work_dir"' EXIT
fake_bin="$work_dir/bin"
mkdir -p "$fake_bin"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local haystack=$1 needle=$2
  [[ $haystack == *"$needle"* ]] || fail "missing [$needle] in [$haystack]"
}

assert_equals() {
  local actual=$1 expected=$2
  [[ $actual == "$expected" ]] || fail "got [$actual], want [$expected]"
}

assert_empty_dir() {
  local dir=$1
  [[ -z $(find "$dir" -mindepth 1 -print -quit) ]] || fail "temporary artifacts remain in $dir"
}

write_fake_tools() {
  cat >"$fake_bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ ${1:-} == ls-files ]] || exit 97
if [[ ${FAKE_GIT_STATUS:-0} != 0 ]]; then
  printf 'fake git failure\n' >&2
  exit "$FAKE_GIT_STATUS"
fi
/bin/cat "$FAKE_GIT_INVENTORY"
EOF
  cat >"$fake_bin/gofmt" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
file=${!#}
printf '<%s>' "$file" >>"$FAKE_GOFMT_CALLS"
if [[ ${FAKE_GOFMT_STATUS:-0} != 0 ]]; then
  printf 'fake gofmt failure\n' >&2
  exit "$FAKE_GOFMT_STATUS"
fi
if [[ ${FAKE_UNFORMATTED:-} == "$file" ]]; then
  printf '%s\n' "$file"
fi
EOF
  cat >"$fake_bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '<%s>' "$*" >>"$FAKE_GO_CALLS"
case "${1:-}" in
  list)
    if [[ ${FAKE_PACKAGE_STATUS:-0} != 0 ]]; then
      printf 'fake package inventory failure\n' >&2
      exit "$FAKE_PACKAGE_STATUS"
    fi
    /bin/cat "$FAKE_PACKAGES"
    ;;
  test)
    shift
    for arg in "$@"; do
      if [[ $arg == -list ]]; then
        if [[ ${FAKE_SERVICE_LIST_STATUS:-0} != 0 ]]; then
          printf 'fake service inventory failure\n' >&2
          exit "$FAKE_SERVICE_LIST_STATUS"
        fi
        /bin/cat "$FAKE_SERVICE_TARGETS"
        exit 0
      fi
    done
    run_pattern=''
    while (($#)); do
      if [[ $1 == -run ]]; then
        run_pattern=$2
        shift 2
      else
        shift
      fi
    done
    if [[ ${FAKE_GO_SLEEP:-0} != 0 ]]; then
      sleep 20
    fi
    if [[ -n ${FAKE_COMMAND_LOG:-} ]]; then
      printf '%b\n' "$FAKE_COMMAND_LOG"
    fi
    if [[ -z $run_pattern ]]; then
      exit "${FAKE_NON_SERVICE_STATUS:-0}"
    fi
    for token in ${FAKE_FAIL_TARGETS:-}; do
      if [[ $run_pattern == *"$token"* ]]; then
        exit 1
      fi
    done
    ;;
  *) exit 98 ;;
esac
EOF
  cat >"$fake_bin/tee" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
log_file=$1
/bin/cat >"$log_file"
/bin/cat "$log_file"
exit "${FAKE_TEE_STATUS:-0}"
EOF
  chmod +x "$fake_bin/git" "$fake_bin/gofmt" "$fake_bin/go" "$fake_bin/tee"
}

write_fake_tools

format_inventory="$work_dir/format-inventory"
format_calls="$work_dir/format-calls"
format_temp="$work_dir/format-temp"
mkdir "$format_temp"
printf 'dir/space name.go\000dir/line\nbreak.go\000' >"$format_inventory"
: >"$format_calls"

format_output=$(CI_FORMAT_GIT="$fake_bin/git" CI_FORMAT_GOFMT="$fake_bin/gofmt" \
  FAKE_GIT_INVENTORY="$format_inventory" FAKE_GOFMT_CALLS="$format_calls" \
  CI_FORMAT_TEMP_PARENT="$format_temp" bash scripts/ci-format.sh 2>&1) || fail "NUL-safe format success case failed: $format_output"
assert_contains "$(<"$format_calls")" 'dir/space name.go'
assert_contains "$(<"$format_calls")" $'dir/line\nbreak.go'
assert_empty_dir "$format_temp"

: >"$format_calls"
if format_output=$(set -o pipefail; FAKE_GIT_INVENTORY="$format_inventory" \
  "$fake_bin/git" ls-files -z -- '*.go' | CI_FORMAT_GOFMT="$fake_bin/gofmt" \
  FAKE_GOFMT_CALLS="$format_calls" CI_FORMAT_TEMP_PARENT="$format_temp" \
  bash scripts/ci-format.sh --stdin 2>&1); then
  :
else
  fail "NUL-safe formatting pipeline failed: $format_output"
fi
assert_contains "$(<"$format_calls")" $'dir/line\nbreak.go'
assert_empty_dir "$format_temp"

: >"$format_inventory"
: >"$format_calls"
format_output=$(CI_FORMAT_GIT="$fake_bin/git" CI_FORMAT_GOFMT="$fake_bin/gofmt" \
  FAKE_GIT_INVENTORY="$format_inventory" FAKE_GOFMT_CALLS="$format_calls" \
  CI_FORMAT_TEMP_PARENT="$format_temp" bash scripts/ci-format.sh 2>&1) || fail "empty tracked-file inventory failed: $format_output"
assert_equals "$(<"$format_calls")" ''
assert_empty_dir "$format_temp"

printf 'bad file.go\000' >"$format_inventory"
: >"$format_calls"
if format_output=$(CI_FORMAT_GIT="$fake_bin/git" CI_FORMAT_GOFMT="$fake_bin/gofmt" \
  FAKE_GIT_INVENTORY="$format_inventory" FAKE_GOFMT_CALLS="$format_calls" \
  FAKE_UNFORMATTED='bad file.go' CI_FORMAT_TEMP_PARENT="$format_temp" \
  bash scripts/ci-format.sh 2>&1); then
  fail 'unformatted tracked Go file unexpectedly passed'
fi
assert_contains "$format_output" 'bad file.go'
assert_empty_dir "$format_temp"

if format_output=$(CI_FORMAT_GIT="$fake_bin/git" CI_FORMAT_GOFMT="$fake_bin/gofmt" \
  FAKE_GIT_INVENTORY="$format_inventory" FAKE_GOFMT_CALLS="$format_calls" \
  FAKE_GIT_STATUS=17 CI_FORMAT_TEMP_PARENT="$format_temp" bash scripts/ci-format.sh 2>&1); then
  fail 'git inventory failure unexpectedly passed'
fi
assert_contains "$format_output" 'fake git failure'
assert_empty_dir "$format_temp"

if format_output=$(set -o pipefail; FAKE_GIT_INVENTORY="$format_inventory" \
  FAKE_GIT_STATUS=23 "$fake_bin/git" ls-files -z -- '*.go' 2>"$work_dir/pipeline-git-error" | \
  CI_FORMAT_GOFMT="$fake_bin/gofmt" FAKE_GOFMT_CALLS="$format_calls" \
  CI_FORMAT_TEMP_PARENT="$format_temp" bash scripts/ci-format.sh --stdin 2>&1); then
  fail 'git pipeline failure unexpectedly passed'
fi
assert_contains "$(<"$work_dir/pipeline-git-error")" 'fake git failure'
assert_empty_dir "$format_temp"

if format_output=$(CI_FORMAT_GIT="$fake_bin/git" CI_FORMAT_GOFMT="$fake_bin/gofmt" \
  FAKE_GIT_INVENTORY="$format_inventory" FAKE_GOFMT_CALLS="$format_calls" \
  FAKE_GOFMT_STATUS=19 CI_FORMAT_TEMP_PARENT="$format_temp" bash scripts/ci-format.sh 2>&1); then
  fail 'gofmt failure unexpectedly passed'
fi
assert_contains "$format_output" 'gofmt failed for tracked Go file'
assert_empty_dir "$format_temp"

source scripts/ci-test.sh
assert_equals "$(escape_annotation_message $'x%:\r\ny')" 'x%25:%0D%0Ay'
assert_equals "$(escape_annotation_property $'x:y,z%\r\n')" 'x%3Ay%2Cz%25%0D%0A'

service_targets=(TestA TestB)
assigned_targets=(TestA TestA)
if validate_exact_once >/dev/null 2>&1; then
  fail 'duplicate assignment unexpectedly passed'
fi
assigned_targets=(TestA)
if validate_exact_once >/dev/null 2>&1; then
  fail 'assignment gap unexpectedly passed'
fi
assigned_targets=(TestA TestB TestUnknown)
if validate_exact_once >/dev/null 2>&1; then
  fail 'unknown assignment unexpectedly passed'
fi

packages="$work_dir/packages"
targets="$work_dir/targets"
go_calls="$work_dir/go-calls"
test_temp="$work_dir/test-temp"
mkdir "$test_temp"
service_package='github.com/definebusiness/wtree/internal/service'

run_ci_test() {
  local mode=$1
  CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
    FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
    CI_TEST_TEMP_PARENT="$test_temp" "$@"
}

printf '%s\n' 'github.com/example/other-a' 'github.com/example/other-b' "$service_package" >"$packages"
printf '%s\n' TestA ExampleB FuzzC >"$targets"
: >"$go_calls"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  :
else
  fail "normal helper run failed: $ci_output"
fi
assert_contains "$(<"$go_calls")" '-timeout=30m'
for target in TestA ExampleB FuzzC; do
  count=$(grep -o -- "$target" "$go_calls" | wc -l | tr -d ' ')
  assert_equals "$count" 1
done
assert_empty_dir "$test_temp"

: >"$go_calls"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh race 2>&1); then
  :
else
  fail "race helper run failed: $ci_output"
fi
assert_contains "$(<"$go_calls")" '-race'
assert_contains "$(<"$go_calls")" '-timeout=45m'
assert_empty_dir "$test_temp"

printf '%s\n' TestA TestB TestC TestD TestE TestF TestG TestH TestI >"$targets"
: >"$go_calls"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  CI_TEST_SHARDS=4 CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  :
else
  fail "inventory-growth helper run failed: $ci_output"
fi
for target in TestA TestB TestC TestD TestE TestF TestG TestH TestI; do
  count=$(grep -o -- "$target" "$go_calls" | wc -l | tr -d ' ')
  assert_equals "$count" 1
done
assert_empty_dir "$test_temp"

printf '%s\n' TestA TestB >"$targets"
: >"$go_calls"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  CI_TEST_SHARDS=8 CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  :
else
  fail "fewer-than-shards helper run failed: $ci_output"
fi
assert_equals "$(grep -o -- '-run' "$go_calls" | wc -l | tr -d ' ')" 2
assert_empty_dir "$test_temp"

printf '%s\n' TestA TestB TestC TestD >"$targets"
: >"$go_calls"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  FAKE_NON_SERVICE_STATUS=1 FAKE_FAIL_TARGETS='TestA TestB' CI_TEST_SHARDS=2 \
  FAKE_COMMAND_LOG='--- FAIL: TestA\n  ordinary failure' CI_TEST_TEMP_PARENT="$test_temp" \
  bash scripts/ci-test.sh normal 2>&1); then
  fail 'multiple command failures unexpectedly passed'
fi
assert_equals "$(grep -o -- '-run' "$go_calls" | wc -l | tr -d ' ')" 2
assert_contains "$ci_output" 'Windows CI service normal shard 1/2'
assert_contains "$ci_output" 'Windows CI service normal shard 2/2'
assert_empty_dir "$test_temp"

: >"$go_calls"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  FAKE_TEE_STATUS=7 CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  fail 'tee failure unexpectedly passed'
fi
assert_contains "$ci_output" 'log transport status=7'
assert_contains "$ci_output" '::error title='
assert_empty_dir "$test_temp"

for fixture in empty duplicate; do
  if [[ $fixture == empty ]]; then
    : >"$targets"
  else
    printf '%s\n' TestA TestA >"$targets"
  fi
  if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
    FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
    CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
    fail "$fixture service inventory unexpectedly passed"
  fi
  assert_contains "$ci_output" '::error title='
  assert_empty_dir "$test_temp"
done

printf '%s\n' "$service_package" >"$packages"
printf '%s\n' TestA >"$targets"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  fail 'empty non-service inventory unexpectedly passed'
fi
assert_contains "$ci_output" 'no non-service packages found'
assert_empty_dir "$test_temp"

:
 >"$packages"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  fail 'empty package inventory unexpectedly passed'
fi
assert_contains "$ci_output" 'service package must appear exactly once; found 0'
assert_empty_dir "$test_temp"

printf '%s\n' 'github.com/example/other' >"$packages"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  fail 'missing service package inventory unexpectedly passed'
fi
assert_contains "$ci_output" 'service package must appear exactly once; found 0'
assert_empty_dir "$test_temp"

printf '%s\n' 'github.com/example/other' "$service_package" "$service_package" >"$packages"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  fail 'duplicate package inventory unexpectedly passed'
fi
assert_contains "$ci_output" 'duplicate package inventory entry'
assert_empty_dir "$test_temp"

printf '%s\n' 'github.com/example/other' "$service_package" >"$packages"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  FAKE_PACKAGE_STATUS=9 CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal 2>&1); then
  fail 'package inventory command failure unexpectedly passed'
fi
assert_contains "$ci_output" 'fake package inventory failure'
assert_empty_dir "$test_temp"

printf '%s\n' 'github.com/example/other' "$service_package" >"$packages"
if ci_output=$(CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" \
  FAKE_PACKAGES="$packages" FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" \
  FAKE_SERVICE_LIST_STATUS=11 CI_TEST_TEMP_PARENT="$test_temp" \
  bash scripts/ci-test.sh normal 2>&1); then
  fail 'service inventory command failure unexpectedly passed'
fi
assert_contains "$ci_output" 'fake service inventory failure'
assert_empty_dir "$test_temp"

if ci_output=$(CI_TEST_NORMAL_TIMEOUT=0m bash scripts/ci-test.sh normal 2>&1); then
  fail 'invalid timeout unexpectedly passed'
fi
assert_contains "$ci_output" 'invalid Go test timeout'
if ci_output=$(CI_TEST_SHARDS=0 bash scripts/ci-test.sh normal 2>&1); then
  fail 'invalid shard count unexpectedly passed'
fi
assert_contains "$ci_output" 'invalid service shard count'

for log_text in \
  'panic: test timed out after 30m0s\\ngoroutine 1' \
  'panic: unexpected state\\npanic detail' \
  'WARNING: DATA RACE\\nrace detail' \
  '# github.com/example/service\\ncompile detail' \
  'fatal error: unexpected signal\\nfatal detail' \
  '--- FAIL: TestOrdinary\\n  ordinary failure' \
  'ordinary fallback line'; do
  log_file="$work_dir/annotation-log"
  printf '%b\n' "$log_text" >"$log_file"
  annotation=$(emit_failure_annotation 'label:with,metacharacters' "$log_file")
  assert_contains "$annotation" 'title=label%3Awith%2Cmetacharacters'
  assert_contains "$annotation" "${log_text%%\\n*}"
done

pipeline_temp="$work_dir/pipeline-temp"
mkdir "$pipeline_temp"
temp_dir="$pipeline_temp"
tee_bin="$fake_bin/tee"
log_counter=0
if direct_output=$(FAKE_TEE_STATUS=7 run_and_annotate 'command-and-tee failure' \
  bash -c 'printf "panic: test timed out after 1s\\n"; exit 13' 2>&1); then
  fail 'combined command and tee failure unexpectedly passed'
else
  direct_status=$?
fi
assert_equals "$direct_status" 13
assert_contains "$direct_output" 'command status=13 log transport status=7'
if direct_output=$(FAKE_TEE_STATUS=7 run_and_annotate 'tee-only failure' \
  bash -c 'printf "ordinary output\\n"' 2>&1); then
  fail 'tee-only failure unexpectedly passed'
else
  direct_status=$?
fi
assert_equals "$direct_status" 7
assert_contains "$direct_output" 'command status=0 log transport status=7'

printf '%s\n' 'github.com/example/other' "$service_package" >"$packages"
printf '%s\n' TestA >"$targets"
: >"$go_calls"
CI_TEST_GO="$fake_bin/go" CI_TEST_TEE="$fake_bin/tee" FAKE_PACKAGES="$packages" \
  FAKE_SERVICE_TARGETS="$targets" FAKE_GO_CALLS="$go_calls" FAKE_GO_SLEEP=1 \
  CI_TEST_TEMP_PARENT="$test_temp" bash scripts/ci-test.sh normal >"$work_dir/interrupted-output" 2>&1 &
helper_pid=$!
for _ in {1..40}; do
  [[ -n $(find "$test_temp" -mindepth 1 -print -quit) ]] && break
  sleep 0.05
done
kill -TERM "$helper_pid"
if wait "$helper_pid"; then
  fail 'interrupted helper unexpectedly passed'
fi
assert_empty_dir "$test_temp"

if bash scripts/ci-test.sh invalid >/dev/null 2>&1; then
  fail 'invalid mode unexpectedly passed'
fi

printf 'PASS: deterministic CI helper harness\n'
