#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    echo "usage: $0 vMAJOR.MINOR.PATCH" >&2
    exit 2
fi

version=$1

if ! printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'; then
    echo "release version must be a semver tag such as v1.2.3: $version" >&2
    exit 1
fi

if ! grep -Eq "^## ${version}([[:space:]]|$)" CHANGELOG.md; then
    echo "CHANGELOG.md is missing a section for ${version}" >&2
    exit 1
fi
