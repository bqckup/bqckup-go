# Simplified README Design

**Date:** 2026-07-31  
**Status:** Approved for implementation

## Goal

Replace the detailed project README with a very short entry point while keeping detailed guidance in `docs/`.

## Structure

The README contains only:

1. project name and one-sentence purpose;
2. one build command;
3. the six supported CLI command forms;
4. one verification command;
5. links to configuration, development, and intern-backlog documentation.

## Constraints

- Keep the exact command names required by `scripts/check-docs.sh`.
- Do not duplicate architecture, feature status, exclusions, or migration details.
- Do not change CLI behavior, config, source code, or the documentation checker.
- Keep Markdown readable in a single screen.

## Verification and integration

Run `make verify` and `sh scripts/check-docs.sh`. Commit the README change on `feat/core-cli-foundation`, push it, merge GitHub PR #1 into `main`, then update the local `main` checkout with a fast-forward pull.
