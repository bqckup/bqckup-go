#!/bin/sh
set -eu

failed=0
required_files="
README.md
docs/architecture.md
docs/configuration-v2.md
docs/development.md
docs/testing.md
docs/migration-from-python.md
docs/intern-backlog.md
"

for file in $required_files; do
    if [ ! -f "$file" ]; then
        echo "missing documentation: $file" >&2
        failed=1
    fi
done

if [ "$failed" -ne 0 ]; then
    exit 1
fi

for command in "bqckup init" "bqckup config validate" "bqckup backup list" "bqckup backup run" "bqckup history list" "bqckup version"; do
    if ! grep -Fq "$command" README.md; then
        echo "README is missing command: $command" >&2
        failed=1
    fi
done

for milestone in M01 M02 M03 M04 M05 M06 M07 M08 M09 M10 M11; do
    if ! grep -Fq "## $milestone" docs/intern-backlog.md; then
        echo "intern backlog is missing milestone: $milestone" >&2
        failed=1
    fi
done

if ! grep -qs '^app:' configs/bqckup.yaml || ! grep -qs '^site:' configs/sites/*.yaml; then
    echo "example configuration is missing the root or site document" >&2
    failed=1
fi

if grep -RniE '^[[:space:]]*(password|access_key|secret_key):[[:space:]]*[^[:space:]]' configs; then
    echo "example configuration contains an inline credential" >&2
    failed=1
fi

if grep -RniE '^[[:space:]]*(access_key_id|secret_access_key):[[:space:]]*[^[:space:]]' configs | grep -vE ':[[:space:]]*EXAMPLE_[A-Z0-9_]+[[:space:]]*$'; then
    echo "storage examples may contain only EXAMPLE_ credential placeholders" >&2
    failed=1
fi

if find internal cmd -name '*.go' -type f -exec grep -liE 'rustic|restic' {} + | grep -q .; then
    echo "Rustic or Restic implementation is outside the foundation scope" >&2
    failed=1
fi

if ! grep -Fq 'Restic is not part of the foundation' docs/intern-backlog.md; then
    echo "intern backlog must state the Restic scope boundary" >&2
    failed=1
fi

for database_contract in 'engine: mysql' 'engine: postgres' 'MYSQL_PWD' 'PGPASSWORD' 'databases/application-mysql.sql.gz'; do
    if ! grep -Rqs "$database_contract" README.md docs; then
        echo "database documentation is missing: $database_contract" >&2
        failed=1
    fi
done

if grep -RniE '^[[:space:]]*password:[[:space:]]*[^<[:space:]][^[:space:]]*' configs; then
    echo "tracked configuration contains a non-placeholder database password" >&2
    failed=1
fi

exit "$failed"
