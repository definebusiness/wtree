#!/usr/bin/env bash
# Run the Windows CI test inventory in bounded, exact-once service shards.
set -euo pipefail

declare -A service_subprocess_helpers=()

escape_annotation_message() {
  local value=$1
  value=${value//'%'/'%25'}
  value=${value//$'\r'/'%0D'}
  value=${value//$'\n'/'%0A'}
  printf '%s' "$value"
}

escape_annotation_property() {
  local value
  value=$(escape_annotation_message "$1")
  value=${value//':'/'%3A'}
  value=${value//','/'%2C'}
  printf '%s' "$value"
}

emit_failure_message() {
  local label=$1
  local message=$2
  printf '::error title=%s::%s\n' \
    "$(escape_annotation_property "$label")" \
    "$(escape_annotation_message "${message:0:4000}")"
}

failure_excerpt() {
  local log_file=$1
  local excerpt
  excerpt=$(awk '
    /panic: test timed out after|^panic:|WARNING: DATA RACE|fatal error:|^# |^go: / {
      print
      context = 6
      next
    }
    /^--- FAIL:/ {
      print
      context = 6
      next
    }
    /^FAIL([[:space:]]|$)/ {
      print
      next
    }
    context > 0 {
      print
      context--
    }
  ' "$log_file" | tail -n 12 || true)
  if [[ -z $excerpt ]]; then
    excerpt=$(tail -n 12 "$log_file" || true)
  fi
  printf '%s' "$excerpt"
}

emit_failure_annotation() {
  local label=$1
  local log_file=$2
  emit_failure_message "$label" "$(failure_excerpt "$log_file")"
}

validate_exact_once() {
  local -A expected=()
  local -A seen=()
  local target
  for target in "${service_targets[@]}"; do
    if [[ -n ${expected[$target]+present} ]]; then
      printf 'duplicate service test name: %s\n' "$target" >&2
      return 1
    fi
    expected[$target]=1
  done
  for target in "${assigned_targets[@]}"; do
    if [[ -z ${expected[$target]+present} ]]; then
      printf 'service assignment contains unknown target: %s\n' "$target" >&2
      return 1
    fi
    if [[ -n ${seen[$target]+present} ]]; then
      printf 'duplicate service assignment: %s\n' "$target" >&2
      return 1
    fi
    seen[$target]=1
  done
  for target in "${service_targets[@]}"; do
    if [[ -z ${seen[$target]+present} ]]; then
      printf 'service assignment missing target: %s\n' "$target" >&2
      return 1
    fi
  done
}

build_service_shards() {
  local shard index target pattern
  shard_patterns=()
  assigned_targets=()
  for ((shard = 0; shard < shard_count; shard++)); do
    shard_targets=()
    for ((index = shard; index < ${#service_targets[@]}; index += shard_count)); do
      target=${service_targets[index]}
      shard_targets+=("$target")
      assigned_targets+=("$target")
    done
    ((${#shard_targets[@]} == 0)) && continue
    pattern="^($(IFS='|'; printf '%s' "${shard_targets[*]}"))$"
    shard_patterns+=("$pattern")
  done
  validate_exact_once
}

# The tracked inventory is shared with the local runner. It records exact
# helper/ordinary-parent pairs; unlisted helper-like names remain schedulable.
load_service_subprocess_helpers() {
  local inventory_file=${CI_TEST_HELPER_INVENTORY:-}
  if [[ -z $inventory_file ]]; then
    local script_root
    script_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
    inventory_file="$script_root/tools/test-runner/service-subprocess-helpers.tsv"
  fi
  [[ -r $inventory_file ]] || {
    printf 'cannot read service subprocess helper inventory: %s\n' "$inventory_file" >&2
    return 1
  }
  service_subprocess_helpers=()
  local helper parent extra
  while IFS=$'\t' read -r helper parent extra || [[ -n ${helper:-}${parent:-}${extra:-} ]]; do
    [[ -z ${helper:-} || $helper == \#* ]] && continue
    if [[ -z ${parent:-} || -n ${extra:-} || ! $helper =~ ^(Test|Example|Fuzz) || ! $parent =~ ^(Test|Example|Fuzz) ]]; then
      printf 'invalid service subprocess helper inventory entry: %s\n' "$helper" >&2
      return 1
    fi
    service_subprocess_helpers["$helper"]=1
  done <"$inventory_file"
  ((${#service_subprocess_helpers[@]} > 0)) || {
    printf 'service subprocess helper inventory is empty\n' >&2
    return 1
  }
}

is_service_subprocess_helper() {
  [[ -n ${service_subprocess_helpers[$1]+present} ]]
}

run_and_annotate() {
  local label=$1
  shift
  local log_file="$temp_dir/log-$((log_counter++))"
  local -a pipeline_status
  local command_status tee_status

  set +e
  "$@" 2>&1 | "$tee_bin" "$log_file"
  pipeline_status=("${PIPESTATUS[@]}")
  set -e
  command_status=${pipeline_status[0]:-1}
  tee_status=${pipeline_status[1]:-1}
  if ((command_status == 0 && tee_status == 0)); then
    return 0
  fi

  printf 'CI command status=%d log transport status=%d: %s\n' \
    "$command_status" "$tee_status" "$label" >&2
  emit_failure_annotation "$label" "$log_file"
  if ((command_status != 0)); then
    return "$command_status"
  fi
  return "$tee_status"
}

fatal_with_annotation() {
  local label=$1
  local exit_status=$2
  local message=$3
  printf '%s\n' "$message" >&2
  emit_failure_message "$label" "$message"
  exit "$exit_status"
}

main() {
  local mode=${1:-}
  local test_timeout
  local -a test_args=()
  case "$mode" in
    normal)
      test_timeout=${CI_TEST_NORMAL_TIMEOUT:-30m}
      ;;
    race)
      test_timeout=${CI_TEST_RACE_TIMEOUT:-45m}
      test_args=(-race)
      ;;
    *)
      printf 'usage: %s normal|race\n' "$0" >&2
      return 2
      ;;
  esac

  if [[ ! $test_timeout =~ ^[1-9][0-9]*[smh]$ ]]; then
    fatal_with_annotation "Windows CI $mode invocation" 2 \
      "invalid Go test timeout: $test_timeout"
  fi

  shard_count=${CI_TEST_SHARDS:-8}
  if [[ ! $shard_count =~ ^[1-9][0-9]*$ ]]; then
    fatal_with_annotation "Windows CI $mode invocation" 2 \
      "invalid service shard count: $shard_count"
  fi

  go_bin=${CI_TEST_GO:-go}
  tee_bin=${CI_TEST_TEE:-tee}
  temp_parent=${CI_TEST_TEMP_PARENT:-${TMPDIR:-/tmp}}
  temp_dir=$(mktemp -d "${temp_parent%/}/wtree-ci-test.XXXXXX")
  log_counter=0
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  if ! load_service_subprocess_helpers; then
    fatal_with_annotation "Windows CI $mode helper inventory" 1 \
      'service subprocess helper inventory cannot be read'
  fi

  local package_inventory="$temp_dir/packages"
  if "$go_bin" list ./... >"$package_inventory" 2>&1; then
    :
  else
    local inventory_status=$?
    fatal_with_annotation "Windows CI $mode package inventory" "$inventory_status" \
      "$(<"$package_inventory")"
  fi

  local service_package=github.com/definebusiness/wtree/internal/service
  local -A package_seen=()
  local -a other_packages=()
  local package
  local service_count=0
  while IFS= read -r package || [[ -n $package ]]; do
    [[ -z $package ]] && continue
    if [[ -n ${package_seen[$package]+present} ]]; then
      fatal_with_annotation "Windows CI $mode package inventory" 1 \
        "duplicate package inventory entry: $package"
    fi
    package_seen[$package]=1
    if [[ $package == "$service_package" ]]; then
      service_count=$((service_count + 1))
    else
      other_packages+=("$package")
    fi
  done <"$package_inventory"
  if ((service_count != 1)); then
    fatal_with_annotation "Windows CI $mode package inventory" 1 \
      "service package must appear exactly once; found $service_count"
  fi
  if ((${#other_packages[@]} == 0)); then
    fatal_with_annotation "Windows CI $mode package inventory" 1 \
      'no non-service packages found'
  fi

  local result=0
  if ! run_and_annotate "Windows CI $mode non-service packages" \
    "$go_bin" test "${test_args[@]}" "-timeout=$test_timeout" "${other_packages[@]}"; then
    result=1
  fi

  local service_inventory="$temp_dir/service-targets"
  if "$go_bin" test -list '^(Test|Example|Fuzz)' ./internal/service >"$service_inventory" 2>&1; then
    :
  else
    local inventory_status=$?
    fatal_with_annotation "Windows CI $mode service inventory" "$inventory_status" \
      "$(<"$service_inventory")"
  fi

  service_targets=()
  local target
  while IFS= read -r target || [[ -n $target ]]; do
    case "$target" in
      Test*|Example*|Fuzz*)
        if ! is_service_subprocess_helper "$target"; then
          service_targets+=("$target")
        fi
        ;;
    esac
  done <"$service_inventory"
  if ((${#service_targets[@]} == 0)); then
    fatal_with_annotation "Windows CI $mode service inventory" 1 \
      'no service tests, examples, or fuzz targets found'
  fi
  if ! build_service_shards; then
    fatal_with_annotation "Windows CI $mode service partition" 1 \
      'service inventory cannot be assigned exactly once'
  fi

  local pattern shard_number=0
  for pattern in "${shard_patterns[@]}"; do
    shard_number=$((shard_number + 1))
    printf 'service %s shard %d/%d: %s\n' \
      "$mode" "$shard_number" "$shard_count" "$pattern"
    if ! run_and_annotate "Windows CI service $mode shard $shard_number/$shard_count" \
      "$go_bin" test "${test_args[@]}" "-timeout=$test_timeout" \
      -run "$pattern" ./internal/service; then
      result=1
    fi
  done
  return "$result"
}

cleanup() {
  local exit_status=$?
  if [[ -n ${temp_dir:-} && -d $temp_dir ]]; then
    case "$temp_dir" in
      "${temp_parent%/}"/wtree-ci-test.*) rm -rf -- "$temp_dir" ;;
      *) printf 'refusing to remove unexpected CI test temporary directory: %s\n' "$temp_dir" >&2 ;;
    esac
  fi
  exit "$exit_status"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  main "$@"
fi
