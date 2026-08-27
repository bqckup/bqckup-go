#!/bin/sh
set -eu

failed=0

required_files="
README.md
USER-GUIDE.md
CHANGELOG.md
"

for file in $required_files; do
    if [ ! -f "$file" ]; then
        echo "missing documentation: $file" >&2
        failed=1
    fi
done

for command in \
    "bqckup init" \
    "bqckup config validate" \
    "bqckup doctor" \
    "bqckup backup list" \
    "bqckup backup run" \
    "bqckup backup unlock" \
    "bqckup history list" \
    "bqckup version"; do
    if ! grep -Fq "$command" README.md; then
        echo "README is missing command: $command" >&2
        failed=1
    fi
done

for contract in \
    "backup_mode: incremental" \
    "password_env:" \
    "engine: mysql" \
    "engine: postgres" \
    "MYSQL_PWD" \
    "PGPASSWORD" \
    "databases/application-mysql.sql.gz" \
    "mode \`0600\`"; do
    if ! grep -Fq "$contract" USER-GUIDE.md; then
        echo "user guide is missing contract: $contract" >&2
        failed=1
    fi
done

if ! grep -Fq '(USER-GUIDE.md)' README.md; then
    echo "README must link to USER-GUIDE.md" >&2
    failed=1
fi

if ! grep -Fq '(CHANGELOG.md)' README.md; then
    echo "README must link to CHANGELOG.md" >&2
    failed=1
fi

# architecture.md, configuration-v2.md, and intern-backlog.md were restored
# after fa56df6 removed them; only genuinely absent docs stay banned.
if grep -RniE 'docs/(development|testing|migration-from-python|guides|superpowers)|tasks/(plan|todo)' \
    README.md USER-GUIDE.md scripts .agents internal; then
    echo "repository still references removed documentation" >&2
    failed=1
fi

if grep -nE '^[[:space:]]*version:[[:space:]]*2[[:space:]]*$' \
    USER-GUIDE.md configs/bqckup.yaml configs/sites/*.yaml; then
    echo "active YAML examples must omit the implicit version field" >&2
    failed=1
fi

if ! grep -qs '^app:' configs/bqckup.yaml || ! grep -qs '^site:' configs/sites/*.yaml; then
    echo "example configuration is missing the root or site document" >&2
    failed=1
fi

if grep -RniE '^[[:space:]]*(password|access_key|secret_key):[[:space:]]*[^[:space:]]' configs; then
    echo "example configuration contains an inline credential" >&2
    failed=1
fi

if grep -RniE '^[[:space:]]*(access_key_id|secret_access_key):[[:space:]]*[^[:space:]]' configs \
    | grep -vE ':[[:space:]]*EXAMPLE_[A-Z0-9_]+[[:space:]]*$'; then
    echo "storage examples may contain only EXAMPLE_ credential placeholders" >&2
    failed=1
fi

if find internal cmd -name '*.go' -type f -exec grep -liE 'rustic' {} + | grep -q .; then
    echo "Rustic implementation is outside the project scope" >&2
    failed=1
fi

exit "$failed"
