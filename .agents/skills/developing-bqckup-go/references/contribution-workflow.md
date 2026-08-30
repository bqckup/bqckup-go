# Contribution workflow

## Before editing

- Inspect `git status` and preserve unrelated work.
- Trace the current behavior through code, tests, CLI help, templates, and
  examples.
- Check `CHANGELOG.md`, the active branch, and its remote target before
  committing or pushing. Never assume a merged PR still represents HEAD.
- Define observable acceptance criteria and the smallest focused test command.
- Identify failure, cancellation, cleanup, permission, multi-destination, and
  secret-redaction paths relevant to the change.

## Red, green, refactor

1. Add the smallest behavioral test.
2. Run it and confirm it fails because the requested behavior is absent.
3. Implement the smallest complete vertical slice that makes it pass.
4. Refactor only while focused tests remain green.
5. Do not add production stubs, silently accepted configuration, or speculative
   abstractions.

Default tests must not require production services, credentials, fixed host
paths, network access, or execution order. Use temporary directories, fixed
clocks, and fakes only at external boundaries.

Live notification tests must remain explicitly opt-in. A normal `go test` or
`make verify` must never read `/etc/bqckup` and send to a configured webhook or
SMTP server.

## Before handoff

```text
gofmt check
go vet ./...
go test -race ./...
go build ./cmd/bqckup
sh scripts/check-docs.sh
```

`make verify` runs the first four checks. Inspect the final diff for accidental
secrets (including webhook IDs/tokens and signed URLs), broken documentation
links, stale `restic/<site>` paths, environment-reference examples, unrelated
edits, and scope expansion. Validate a real local config only without printing
credential values. If authorized to publish, push code without copying local
`/etc/bqckup` content into the repository.
Report exact command results rather than predictions.
