# ADR-002: Repo Conventions for claude-bot Targets

**Status:** Accepted
**Date:** 2026-02-13

## Context

claude-bot runs Claude Code on repos to fix issues. For it to work well, target repos need some structure. Claude Code looks for `CLAUDE.md` in the repo root, and the quality of the bot's output depends heavily on the instructions in that file.

We need sensible defaults for how repos should be set up so claude-bot produces good results consistently.

## Decision

### Every repo that claude-bot manages MUST have:

1. **`CLAUDE.md`** in the root — tells Claude how to work on this repo:
   - What the project is (one sentence)
   - How to build (`task build` or equivalent)
   - How to test (`task test` or equivalent)
   - Code conventions (language, style, patterns)
   - What NOT to do (don't add deps, don't change API, etc.)

2. **Tests that can run without network** — Claude should be able to verify its changes locally. If there are integration tests, they should be skippable.

3. **A clear issue template** — the better the issue description, the better Claude's fix. Minimum: title + body explaining what's wrong and what the expected behavior is.

### Recommended (not required):

4. **ADRs in `docs/adr/`** — architectural decisions that Claude needs to respect. Claude reads these when exploring the codebase.

5. **Specs in `docs/specs/`** — feature specs or PRDs for larger pieces of work. When an issue references a spec, Claude can read it for full context.

6. **Labels for workflow** — use the default labels (`todo`, `in-progress`, `done`, `needs-info`, `failed`). Don't fight the convention.

7. **Small, focused issues** — one issue = one change. "Fix the login bug" is good. "Refactor the entire auth system" will likely fail or produce partial results.

### CLAUDE.md Template

```markdown
# project-name

## What It Is
One sentence describing the project.

## Build
\`\`\`bash
task build  # or: npm run build, cargo build, etc.
\`\`\`

## Test
\`\`\`bash
task test   # or: npm test, cargo test, etc.
\`\`\`

## Conventions
- Language and version
- Key patterns (e.g., "everything in one file", "use stdlib only")
- Error handling style
- Test expectations

## Don't
- Don't add external dependencies without approval
- Don't change public APIs
- Don't skip tests
```

## Key Points

- **CLAUDE.md is the single most important file** — it's the difference between Claude producing good PRs and garbage
- **Tests are the safety net** — if Claude can run tests, it can self-verify
- **Small issues win** — the bot works best on focused, well-described issues
- **Specs and ADRs scale** — for complex repos, these give Claude the context it needs

## Consequences

- Repos without CLAUDE.md will still work but produce lower quality results
- Teams adopting claude-bot need to invest ~30 minutes writing a good CLAUDE.md
- The convention is lightweight enough that it doesn't impose on teams who don't use the bot
