#!/usr/bin/env bash
# Bounded repository-native documentation check for changed-area docs lanes.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

exec go run ./tools/test-runner docs-check
