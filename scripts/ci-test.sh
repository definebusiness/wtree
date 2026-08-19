#!/usr/bin/env bash
# Keep the Git-heavy service package in bounded test binaries on Windows CI.
set -euo pipefail

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
if package_output="$(go list ./...)"; then
  :
else
  exit $?
fi

other_packages=()
while IFS= read -r package; do
  if [[ -n "$package" && "$package" != "$service_package" ]]; then
    other_packages+=("$package")
  fi
done <<< "$package_output"

if ((${#other_packages[@]} == 0)); then
  printf 'no non-service packages found\n' >&2
  exit 1
fi

result=0
if ! go test "${test_args[@]}" "-timeout=$test_timeout" "${other_packages[@]}"; then
  result=1
fi

service_output=""
if service_output="$(go test -list '^(Test|Example|Fuzz)' ./internal/service)"; then
  :
else
  exit $?
fi

service_tests=()
while IFS= read -r test_name; do
  case "$test_name" in
    Test*|Example*|Fuzz*) service_tests+=("$test_name") ;;
  esac
done <<< "$service_output"

if ((${#service_tests[@]} == 0)); then
  printf 'no service tests, examples, or fuzz targets found\n' >&2
  exit 1
fi

for ((left = 0; left < ${#service_tests[@]}; left++)); do
  for ((right = left + 1; right < ${#service_tests[@]}; right++)); do
    if [[ "${service_tests[left]}" == "${service_tests[right]}" ]]; then
      printf 'duplicate service test name: %s\n' "${service_tests[left]}" >&2
      exit 1
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
  if ! go test "${test_args[@]}" "-timeout=$test_timeout" \
    -run "$shard_pattern" ./internal/service; then
    result=1
  fi
done

if ((assigned_count != ${#service_tests[@]})); then
  printf 'assigned %d of %d service tests/examples/fuzz targets\n' \
    "$assigned_count" "${#service_tests[@]}" >&2
  exit 1
fi

exit "$result"
