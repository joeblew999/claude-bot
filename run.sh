#!/usr/bin/env bash
# Usage: CB_REPOS="owner/repo" ./run.sh
cd "$(dirname "$0")" || exit 1
exec ./claude-bot "$@"
