# Simplified README Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current detailed README with a single-screen project entry point, then merge the verified foundation into `main`.

**Architecture:** Keep detailed guidance in the existing `docs/` tree. The root README remains only a discoverability and command reference layer, while `scripts/check-docs.sh` continues enforcing the supported command names.

**Tech Stack:** Markdown, POSIX shell documentation check, Go verification commands, Git, GitHub CLI.

## Global Constraints

- Keep all six command forms required by `scripts/check-docs.sh`.
- Do not change CLI behavior, Go source, schema-v2 configuration, or the documentation checker.
- Keep the README readable in one screen.
- Merge only after fresh full verification passes.

---

### Task 1: Replace the root README

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: command-name contract from `scripts/check-docs.sh`
- Produces: concise repository entry point linking canonical docs

- [ ] **Step 1: Replace README content**

Use exactly this structure:

````markdown
# Bqckup Go

A Go-based backup CLI built with Cobra, Viper, GORM, and SQLite.

## Build

```bash
go build -o bqckup ./cmd/bqckup
```

## Commands

```text
bqckup init
bqckup config validate
bqckup backup list
bqckup backup run <site> [--force]
bqckup history list [--site <name>] [--limit <n>]
bqckup version
```

## Development

```bash
make verify
```

Documentation: [configuration](docs/configuration-v2.md), [development](docs/development.md), and [intern backlog](docs/intern-backlog.md).
````

- [ ] **Step 2: Verify the documentation contract**

Run: `sh scripts/check-docs.sh`

Expected: exit code `0` with no missing README command.

- [ ] **Step 3: Review the README diff**

Run: `git diff --check && git diff -- README.md`

Expected: only the intentional README simplification and no whitespace errors.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: simplify README"
```

### Task 2: Verify and integrate into main

**Files:**
- Modify only files required to fix a verification defect

**Interfaces:**
- Consumes: committed `feat/core-cli-foundation` branch and GitHub PR #1
- Produces: verified remote and local `main`

- [ ] **Step 1: Run full verification on the feature branch**

Run:

```bash
make verify
sh scripts/check-docs.sh
git diff --check
test -z "$(git status --porcelain)"
```

Expected: all commands exit `0` and the worktree is clean.

- [ ] **Step 2: Push the feature branch**

Run: `git push origin feat/core-cli-foundation`

Expected: remote branch contains the README commit.

- [ ] **Step 3: Merge GitHub PR #1**

Run:

```bash
gh pr ready 1 --repo bqckup/bqckup-go
gh pr merge 1 --repo bqckup/bqckup-go --merge
```

Expected: PR state is `MERGED` with base `main`.

- [ ] **Step 4: Synchronize and verify local main**

From `/home/revv/Development/backup/bqckup-go`, run:

```bash
git pull --ff-only origin main
make verify
sh scripts/check-docs.sh
```

Expected: local `main` matches `origin/main` and all verification commands exit `0`.
