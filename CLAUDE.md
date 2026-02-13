# claude-bot

## What It Is
Single Go binary that polls GitHub repos for `todo`-labeled issues, runs Claude Code on them, and creates PRs.

## Stack
- Go 1.25, stdlib only, zero external deps
- External tools: `gh` (GitHub CLI), `git`, `claude` (Claude Code CLI)

## Build
```bash
go build -o claude-bot .
```

## Run
```bash
REPOS="owner/repo1,owner/repo2" ./claude-bot
```

## Conventions
- Everything in main.go — single file
- Every operation is idempotent (safe to restart)
- No side effects — checks state before acting
- Error handling: wrap with fmt.Errorf
- Logging: log.Printf for summary, per-issue log files for detail
