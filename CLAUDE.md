# claude-bot

## What It Is
Single Go binary that polls GitHub repos for `todo`-labeled issues, runs Claude Code on them, and creates PRs. Also triages new issues and discussions with Claude-powered responses.

## Stack
- Go 1.25, stdlib only, zero external deps
- External tools: `gh` (GitHub CLI), `git`, `claude` (Claude Code CLI)
- Dev tooling: [Taskfile](https://taskfile.dev) — all dev commands go through `task`

## File Structure
- `main.go` — bot logic (config, polling, triage, workers, git ops, GitHub API)
- `self.go` — self-management (build, release, update, version)
- `main_test.go` — unit + integration tests
- `Taskfile.yml` — dev commands (thin wrappers around Go binary subcommands)

## Build & Dev
Always use `task` for dev commands, never raw `go build` / `go test`:
```bash
task            # build (calls go run . --build)
task test       # run unit tests
task release    # cross-compile + publish GitHub release
task update     # self-update from latest release
task start      # build + run (idempotent)
task stop       # stop (idempotent)
task clean      # remove worktrees + logs
```

## Conventions
- Every operation is idempotent (safe to restart at any point)
- No side effects — always checks state before acting
- Error handling: wrap with `fmt.Errorf("context: %w", err)`
- Logging: `log.Printf` for summary, per-issue log files for detail
- Bot comments include `<!-- claude-bot -->` HTML marker for reliable detection
- DRY: binary names, targets, and versions defined once in `self.go`
- Taskfile calls Go binary subcommands, CI calls Taskfile — single source of truth
- `.env` loaded at runtime by the binary itself (not by Taskfile)

## Labels
Auto-created: `todo`, `in-progress`, `done`, `needs-info`, `failed`, `triaged`

## Subcommands
```
--build        Build binary from source (embeds git commit via ldflags)
--release      Cross-compile 6 targets + publish GitHub release
--update       Download latest release and replace ./claude-bot
--clean        Remove worktrees + logs
--clean-all    Full reset
--version      Print version (commit hash + platform)
--help         Print usage
```

## When Making Changes
- Run `task test` after changes — all 13+ tests must pass
- Don't add external dependencies — stdlib only
- Keep things idempotent
- If adding a new env var, prefix with `CB_` and add to `loadConfig()`, `--help`, and README
