# claude-bot

Single Go binary that watches GitHub repos for `todo`-labeled issues, runs Claude Code, and creates PRs.


https://github.com/joeblew999/claude-bot/
https://github.com/joeblew999/claude-bot/releases/tag/latest


## Quick Start

```bash
go build -o claude-bot .
CB_REPOS="owner/repo" ./claude-bot
```

Or use the Taskfile:

```bash
task start    # build + run
task test     # unit tests
task clean    # remove build artifacts + bot state
```

## How It Works

1. Polls repos for issues labeled `todo`
2. Picks up issue, labels it `in-progress`
3. Runs Claude Code in an isolated git worktree
4. Commits, pushes, creates PR
5. Comments PR link on issue, labels `done`

## Triage (optional)

Set `CB_TRIAGE=1` to auto-respond to new unlabeled issues with a friendly message before they become todos.

## Config

All env vars prefixed `CB_`:

| Variable | Default | Description |
|----------|---------|-------------|
| `CB_REPOS` | *(required)* | Comma-separated `owner/repo` list |
| `CB_POLL_INTERVAL` | `30s` | Poll frequency |
| `CB_WORKERS` | `3` | Parallel workers |
| `CB_MAX_RETRIES` | `3` | Failures before marking `failed` |
| `CB_MAX_TURNS` | `50` | Claude `--max-turns` |
| `CB_TRIAGE` | off | Set `1` to enable triage |
| `CB_AUTO_INSTALL` | off | Set `1` to auto-install deps |

## Prerequisites

- Go 1.25+
- `git` with identity configured
- `gh` authenticated (`gh auth login`)
- `claude` CLI authenticated

With `CB_AUTO_INSTALL=1`, missing tools are installed automatically via brew/apt/dnf/npm.

## Labels

Auto-created on startup: `todo`, `in-progress`, `done`, `needs-info`, `failed`, `triaged`.

## Clean Up

```bash
./claude-bot --clean      # remove worktrees + logs
./claude-bot --clean-all  # full reset
```
