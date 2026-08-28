#!/usr/bin/env bash
# Check every tracked Go source file without relying on shell glob expansion.
set -euo pipefail

git_bin=${CI_FORMAT_GIT:-git}
gofmt_bin=${CI_FORMAT_GOFMT:-gofmt}
temp_parent=${CI_FORMAT_TEMP_PARENT:-${TMPDIR:-/tmp}}
temp_dir=$(mktemp -d "${temp_parent%/}/wtree-ci-format.XXXXXX")
inventory_file="$temp_dir/tracked-go-files"

cleanup() {
  local exit_status=$?
  if [[ -n ${temp_dir:-} && -d $temp_dir ]]; then
    case "$temp_dir" in
      "${temp_parent%/}"/wtree-ci-format.*) rm -rf -- "$temp_dir" ;;
      *) printf 'refusing to remove unexpected CI format temporary directory: %s\n' "$temp_dir" >&2 ;;
    esac
  fi
  exit "$exit_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

case ${1:-} in
  '') "$git_bin" ls-files -z -- '*.go' >"$inventory_file" ;;
  --stdin) cat >"$inventory_file" ;;
  *)
    printf 'usage: %s [--stdin]\n' "$0" >&2
    exit 2
    ;;
esac

result=0
while IFS= read -r -d '' file; do
  if unformatted=$("$gofmt_bin" -l -- "$file"); then
    if [[ -n $unformatted ]]; then
      printf '%s\n' "$file"
      result=1
    fi
  else
    exit_status=$?
    printf 'gofmt failed for tracked Go file: %s\n' "$file" >&2
    exit "$exit_status"
  fi
done <"$inventory_file"

exit "$result"
