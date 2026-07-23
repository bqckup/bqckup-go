# Contribution workflow

The canonical sources are [`docs/development.md`](../../../../docs/development.md), [`docs/testing.md`](../../../../docs/testing.md), and [`docs/intern-backlog.md`](../../../../docs/intern-backlog.md).

## Before editing

- Resolve the milestone and prerequisites.
- Inspect `git status`; preserve unrelated user changes.
- Identify public contracts, failure paths, cleanup, and secret surfaces.
- Write concrete acceptance checks.

## Red-green-refactor

- Add the smallest behavioral test first.
- Run it and verify the expected failure, not a syntax/setup accident.
- Implement only enough complete production behavior to turn it green.
- Add cancellation, partial-failure, redaction, permission, and multi-destination cases relevant to the boundary.
- Do not depend on production services, credentials, fixed paths, or network access in the default suite.

## Before handoff

```text
gofmt check
go vet ./...
go test -race ./...
go build ./cmd/bqckup
sh scripts/check-docs.sh
```

Inspect the diff for accidental secrets and scope expansion. Update examples and docs for public behavior. Report evidence and deferred items; never substitute “should pass” for current results.
