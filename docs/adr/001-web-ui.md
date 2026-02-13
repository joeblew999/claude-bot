# ADR-001: Web UI for Issue Management

**Status:** Proposed
**Date:** 2026-02-13

## Context

Users need a simple way to see the state of their issues and manage them (add labels, view progress) without going directly to GitHub. The bot already uses GitHub Issues as its sole state store.

## Decision

Build a lightweight web UI on Cloudflare Workers using:

- **Hono** — minimal web framework for Workers
- **DaisyUI 5** — Tailwind CSS component library for the UI
- **Zod** — input validation
- **GitHub API** — the UI reads/writes issue state via GitHub (no separate database)

GitHub remains the single source of truth. The UI is just a window into it.

## Key Points

- No database — all state lives in GitHub Issues and labels
- The UI talks to the GitHub API to list issues, change labels, view PR status
- Auth via GitHub OAuth (the user's own token, not the bot's)
- Deploy as a Cloudflare Worker (free tier is fine)
- Keep it dead simple — one page showing issues grouped by label (todo, in-progress, done, failed)

## Consequences

- Zero additional state to manage or sync
- Users who don't want to use GitHub directly get a cleaner view
- Cloudflare Workers gives us global edge deployment for free
