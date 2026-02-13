# claude-bot

Single Go binary that watches GitHub repos for `todo`-labeled issues, runs Claude Code on them, and creates PRs — all under your own git identity. No cloud, no bot accounts, no API keys.

## How It Works

```
┌─────────────────────────────────────────────────────┐
│                    POLL LOOP (30s)                   │
│                                                     │
│  gh issue list --label todo --json ... --repo X     │
│  gh issue list --label todo --json ... --repo Y     │
│                                                     │
│  For each issue not already in-progress:            │
│    → Queue it                                       │
└──────────────────────┬──────────────────────────────┘
                       │
          ┌────────────▼────────────┐
          │     WORKER POOL (N=3)   │
          │                         │
          │  Worker 1: issue #42    │
          │  Worker 2: issue #17    │
          │  Worker 3: idle         │
          └────────────┬────────────┘
                       │
         ┌─────────────▼──────────────┐
         │      PER-ISSUE WORKFLOW    │
         │                            │
         │  1. Label → in-progress    │
         │  2. git worktree add       │
         │  3. claude -p "..."        │
         │  4. git add + commit       │
         │  5. git push               │
         │  6. gh pr create           │
         │  7. Comment PR on issue    │
         │  8. Label → done           │
         │  9. git worktree remove    │
         └────────────────────────────┘
```

## Quick Start

```bash
# Edit run.sh to set your repos, then:
./run.sh
```

The launcher builds the binary, auto-installs missing tools, and starts the bot.

## Prerequisites

- **Go 1.25+** (for building)
- **git** with `user.name` and `user.email` configured
- **gh** (GitHub CLI) authenticated via `gh auth login`
- **claude** (Claude Code CLI) authenticated to your subscription

Missing tools are **automatically installed** at startup when `CB_AUTO_INSTALL=1` (the default in `run.sh`):

| Tool | Install method |
|------|---------------|
| `git` | `brew` / `apt` / `dnf` (OS-detected) |
| `gh` | `brew` / `apt` / `dnf` (OS-detected) |
| `claude` | `npm install -g @anthropic-ai/claude-code` |

Things that need human input (git identity, gh auth) are flagged with clear instructions.

## Build & Run (manual)

```bash
go build -o claude-bot .
CB_REPOS="owner/repo1,owner/repo2" ./claude-bot
```

## Configuration

All env vars are prefixed with `CB_` to avoid clashes with other tools:

| Variable | Default | Description |
|----------|---------|-------------|
| `CB_REPOS` | *(required)* | Comma-separated repos to watch (`owner/repo`) |
| `CB_POLL_INTERVAL` | `30s` | How often to check for new issues |
| `CB_WORKERS` | `3` | Max parallel Claude Code instances |
| `CB_ISSUE_LABEL` | `todo` | Label that triggers pickup |
| `CB_WIP_LABEL` | `in-progress` | Label applied while working |
| `CB_DONE_LABEL` | `done` | Label applied when PR is created |
| `CB_WORKTREE_DIR` | `~/.claude-bot/trees` | Where git worktrees live |
| `CB_REPO_DIR` | `~/.claude-bot/repos` | Where repo clones live |
| `CB_LOG_DIR` | `~/.claude-bot/logs` | Per-issue log files |
| `CB_MAX_TURNS` | `50` | Claude Code `--max-turns` flag |
| `CB_AUTO_INSTALL` | *(off)* | Set to `1` to auto-install missing deps |

## Usage

1. Add the `todo` label to an issue on any repo in `CB_REPOS`
2. The bot picks it up within `CB_POLL_INTERVAL`, labels it `in-progress`
3. Claude Code works on the issue in an isolated git worktree
4. Bot commits, pushes, and creates a PR
5. Bot comments the PR link on the issue and labels it `done`
6. You review the PR on GitHub

Everything runs under your identity — commits and PRs show as you, not a bot.

## Idempotency

Every operation is safe to repeat. If you kill the process and restart:

- Issues mid-flight keep their `in-progress` label (poll skips them)
- Existing worktrees get reused, not recreated
- Existing branches get checked out, not re-created
- Existing PRs don't get duplicated
- Existing comments don't get re-posted
- Stale local branches from failed runs are automatically cleaned up

## Error Handling

- **Claude timeout:** Killed after 10 minutes. Issue gets error comment, labels reset to `todo`.
- **Claude no changes:** Issue gets "couldn't resolve" comment, labels reset to `todo`.
- **Any step failure:** Error commented on issue, labels reset, worktree + stale branch cleaned up, worker moves on.

## Running in the Background

```bash
# tmux (recommended — Claude Code needs a TTY-like environment)
tmux new-session -d -s claude-bot './run.sh'

# Or nohup
CB_REPOS="owner/repo" nohup ./claude-bot > ~/.claude-bot/bot.log 2>&1 &
```

## Clean Up

Two clean commands for different purposes:

```bash
# Remove worktrees and logs (keeps repo clones, re-tests deps on next run)
./claude-bot --clean

# Full reset: remove everything including repo clones
./claude-bot --clean-all
```

`--clean` is what you want most of the time — it clears working state so the next run starts fresh, re-checks dependencies, and re-clones repos only if needed.

`--clean-all` is a full factory reset of `~/.claude-bot/`.

## What This Does NOT Do

- No web UI — check GitHub for status
- No database — state is in GitHub labels
- No Docker — runs native
- No API key — uses Claude Max subscription via native CLI auth
- No bot account — everything is your identity
- No VS Code — pure terminal

## CLAUDE.md for Target Repos

Each repo should have a `CLAUDE.md` with project-specific instructions. Claude Code reads it automatically. Example:

```markdown
# Project

## Stack
- Go 1.25, tests: `go test ./...`

## Conventions
- Error handling: wrap with fmt.Errorf
- Table-driven tests
```
