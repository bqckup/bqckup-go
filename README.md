# Bqckup Go

The CLI-first Go rewrite of Bqckup. The current milestone establishes the
tested project foundation and a complete local file-backup workflow. Database
exporters and S3-compatible storage are assigned as independent follow-up
milestones.

## Development

```bash
make verify
go run ./cmd/bqckup version
```

The supported toolchain is Go 1.26 with CGO and GCC for SQLite.
