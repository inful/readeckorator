#!/usr/bin/env bash
# Conventional Commits validator for readeckorator.
#
# Invoked by lefthook's commit-msg hook with the path to the
# commit message file as $1. Exits non-zero on rejection so the
# commit is blocked.
#
# Rules:
#   <type>(<scope>)?: <subject>
#   type ∈ feat|fix|chore|docs|test|refactor|ci|build|perf|style|revert
#   scope ∈ [a-z0-9_-]+
#   optional "!" before ":" marks a breaking change

set -euo pipefail

if [ "$#" -lt 1 ]; then
  echo "commitlint: missing commit message file argument" >&2
  exit 2
fi

msg_file="$1"
msg=$(cat "$msg_file")

# Allow comments and merge messages through.
if [ -z "$msg" ] || echo "$msg" | head -1 | grep -qE '^#'; then
  exit 0
fi

pattern='^(feat|fix|chore|docs|test|refactor|ci|build|perf|style|revert)(\([a-z0-9_-]+\))?!?: .+'

if echo "$msg" | grep -qE "$pattern"; then
  exit 0
fi

cat >&2 <<EOF

commit message must follow Conventional Commits

expected: <type>(<scope>)?: <subject>
allowed types: feat|fix|chore|docs|test|refactor|ci|build|perf|style|revert
breaking changes: append ! before the colon

got:
  $msg

EOF
exit 1
