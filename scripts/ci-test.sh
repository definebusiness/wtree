#!/usr/bin/env bash
# Keep the Git-heavy service package in bounded test binaries on Windows CI.
set -euo pipefail

escape_annotation_message() {
  local value=$1
  value=${value//'%'/'%25'}
  value=${value//$'\r'/'%0D'}
  value=${value//$'\n'/'%0A'}
  printf '%s' "$value"
}

escape_annotation_property() {
  local value=$1
  value=$(escape_annotation_message "$value")
  value=${value//':'/'%3A'}
  value=${value//','/'%2C'}
  printf '%s' "$value"
}

emit_failure_message() {
  local label=$1
  local message=$2
  printf '::error title=%s::%s\n' \
    "$(escape_annotation_property "$label")" \
    "$(escape_annotation_message "$message")"
}

fail_with_annotation() {
  local label=$1
  local status=$2
  local message=$3
  printf '%s\n' "$message" >&2
  emit_failure_message "$label" "${message:0:4000}"
  exit "$status"
}

emit_failure_annotation() {
  local label=$1
  local log_file=$2
  local excerpt
  excerpt=$(awk '
    /panic: test timed out after/ {
      print
      context = "timeout"
      remaining = 4
      next
    }
    /^--- FAIL:/ {
      print
      context = "failure"
      remaining = 6
      next
    }
    /^FAIL([[:space:]]|$)|WARNING: DATA RACE|fatal error:|^go: |^# / {
      print
      context = ""
      next
    }
    context == "timeout" &&
      ($0 ~ /^[[:space:]]+running tests:$/ ||
       $0 ~ /^[[:space:]]+(Test|Example|Fuzz)[^[:space:]]*[[:space:]]+\(/) {
      print
      if (--remaining == 0) {
        context = ""
      }
      next
    }
    context == "failure" && $0 ~ /^[[:space:]]+/ &&
      $0 !~ /^[[:space:]]*goroutine [[:digit:]]+ / &&
      $0 !~ /^[[:space:]]*[^[:space:]]+\.go:[[:digit:]]+[[:space:]]+\+0x/ {
      print
      if (--remaining == 0) {
        context = ""
      }
      next
    }
    {
      context = ""
    }
  ' "$log_file" | tail -n 12 || true)
  if [[ -z "$excerpt" ]]; then
    excerpt=$(tail -n 12 "$log_file")
  fi
  excerpt=${excerpt:0:4000}
  emit_failure_message "$label" "$excerpt"
}

run_and_annotate() {
  local label=$1
  shift
  local log_file
  local -a pipeline_status
  local command_status
  local tee_status
  log_file=$(mktemp)

  set +e
  "$@" 2>&1 | tee "$log_file"
  pipeline_status=("${PIPESTATUS[@]}")
  set -e
  command_status=${pipeline_status[0]}
  tee_status=${pipeline_status[1]}

  if ((command_status == 0 && tee_status == 0)); then
    rm -f "$log_file"
    return 0
  fi
  if ((command_status == 0)); then
    command_status=$tee_status
  fi
  emit_failure_annotation "$label" "$log_file"
  rm -f "$log_file"
  return "$command_status"
}

mode=${1:-}
case "$mode" in
  normal)
    test_timeout=30m
    test_args=()
    ;;
  race)
    test_timeout=45m
    test_args=(-race)
    ;;
  *)
    printf 'usage: %s normal|race\n' "$0" >&2
    exit 2
    ;;
esac

service_package=github.com/definebusiness/wtree/internal/service
package_output=""
if package_output="$(go list ./... 2>&1)"; then
  :
else
  status=$?
  fail_with_annotation "Windows CI $mode package inventory" "$status" "$package_output"
fi

other_packages=()
while IFS= read -r package; do
  if [[ -n "$package" && "$package" != "$service_package" ]]; then
    other_packages+=("$package")
  fi
done <<< "$package_output"

if ((${#other_packages[@]} == 0)); then
  fail_with_annotation "Windows CI $mode package inventory" 1 \
    'no non-service packages found'
fi

result=0
if ! run_and_annotate "Windows CI $mode non-service packages" \
  go test "${test_args[@]}" "-timeout=$test_timeout" "${other_packages[@]}"; then
  result=1
fi

service_output=""
if service_output="$(go test -list '^(Test|Example|Fuzz)' ./internal/service 2>&1)"; then
  :
else
  status=$?
  fail_with_annotation "Windows CI $mode service inventory" "$status" "$service_output"
fi

service_tests=()
while IFS= read -r test_name; do
  case "$test_name" in
    Test*|Example*|Fuzz*) service_tests+=("$test_name") ;;
  esac
done <<< "$service_output"

if ((${#service_tests[@]} == 0)); then
  fail_with_annotation "Windows CI $mode service inventory" 1 \
    'no service tests, examples, or fuzz targets found'
fi

for ((left = 0; left < ${#service_tests[@]}; left++)); do
  for ((right = left + 1; right < ${#service_tests[@]}; right++)); do
    if [[ "${service_tests[left]}" == "${service_tests[right]}" ]]; then
      message="duplicate service test name: ${service_tests[left]}"
      fail_with_annotation "Windows CI $mode service inventory" 1 "$message"
    fi
  done
done

shard_count=8
assigned_count=0
for ((shard = 0; shard < shard_count; shard++)); do
  shard_tests=()
  for ((index = shard; index < ${#service_tests[@]}; index += shard_count)); do
    shard_tests+=("${service_tests[index]}")
  done
  ((${#shard_tests[@]} > 0)) || continue

  assigned_count=$((assigned_count + ${#shard_tests[@]}))
  shard_pattern=""
  printf -v shard_pattern '^%s$' "$(IFS='|'; printf '(%s)' "${shard_tests[*]}")"
  printf 'service %s shard %d/%d: %d top-level tests/examples/fuzz targets\n' \
    "$mode" "$((shard + 1))" "$shard_count" "${#shard_tests[@]}"
  if ! run_and_annotate "Windows CI service $mode shard $((shard + 1))/$shard_count" \
    go test "${test_args[@]}" "-timeout=$test_timeout" \
      -run "$shard_pattern" ./internal/service; then
    result=1
  fi
done

if ((assigned_count != ${#service_tests[@]})); then
  message="assigned $assigned_count of ${#service_tests[@]} service tests/examples/fuzz targets"
  fail_with_annotation "Windows CI $mode service partition" 1 "$message"
fi

exit "$result"
