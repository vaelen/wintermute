#!/usr/bin/env bash
# Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
# SPDX-License-Identifier: MIT
#
# check-headers.sh: verify that every tracked source file in this repo carries
# the MIT SPDX-License-Identifier line.
#
# Exits non-zero with a list of offenders if any source file is missing the
# header.

set -euo pipefail

SPDX="SPDX-License-Identifier: MIT"

# Source patterns we require headers on. Add new extensions here as needed.
# Vendored dependencies, generated files, and go.mod/go.sum are exempt.
PATTERNS=(
  '*.go'
  '*.lua'
  '*.sql'
  '*.sh'
)

# Exclusions (paths matched against the repo-relative path with `git ls-files`).
EXCLUDE_REGEX='^(vendor/|.*\.pb\.go$|.*_generated\.go$)'

cd "$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)"

declare -a candidates=()
for pat in "${PATTERNS[@]}"; do
  while IFS= read -r f; do
    [[ -z "$f" ]] && continue
    [[ "$f" =~ $EXCLUDE_REGEX ]] && continue
    candidates+=("$f")
  done < <(git ls-files -- "$pat" 2>/dev/null || true)
done

# Also enforce on .toml and .yml files that are clearly ours (root + scripts).
# We can't use the same blanket as above because dotfiles like .golangci.yml
# need the header but vendor configs (if any) shouldn't.
for f in $(git ls-files -- '*.toml' '*.yml' 2>/dev/null || true); do
  case "$f" in
    .github/*|vendor/*) continue ;;
  esac
  candidates+=("$f")
done

# The Makefile itself.
if git ls-files Makefile >/dev/null 2>&1; then
  candidates+=(Makefile)
fi

missing=()
for f in "${candidates[@]}"; do
  if ! grep -q "$SPDX" "$f"; then
    missing+=("$f")
  fi
done

if (( ${#missing[@]} > 0 )); then
  printf 'Missing %s header in:\n' "$SPDX" >&2
  for f in "${missing[@]}"; do
    printf '  %s\n' "$f" >&2
  done
  exit 1
fi

printf 'check-headers: %d files OK\n' "${#candidates[@]}"
