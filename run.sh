#!/usr/bin/env bash
# claude-bot launcher
# Usage: ./run.sh
#
# All configuration via CB_ prefixed env vars.
# Edit the values below or export them before running.

# Required: comma-separated repos to watch
export CB_REPOS="${CB_REPOS:-joeblew999/claude-bot}"

# Optional: tune these as needed
export CB_POLL_INTERVAL="${CB_POLL_INTERVAL:-30s}"
export CB_WORKERS="${CB_WORKERS:-3}"
export CB_MAX_TURNS="${CB_MAX_TURNS:-50}"

# Labels
export CB_ISSUE_LABEL="${CB_ISSUE_LABEL:-todo}"
export CB_WIP_LABEL="${CB_WIP_LABEL:-in-progress}"
export CB_DONE_LABEL="${CB_DONE_LABEL:-done}"

# Directories
export CB_WORKTREE_DIR="${CB_WORKTREE_DIR:-$HOME/.claude-bot/trees}"
export CB_REPO_DIR="${CB_REPO_DIR:-$HOME/.claude-bot/repos}"
export CB_LOG_DIR="${CB_LOG_DIR:-$HOME/.claude-bot/logs}"

# Auto-install missing deps (git, gh, claude)
export CB_AUTO_INSTALL="${CB_AUTO_INSTALL:-1}"

# Build and run
cd "$(dirname "$0")" || exit 1
go build -o claude-bot . || exit 1
exec ./claude-bot "$@"
