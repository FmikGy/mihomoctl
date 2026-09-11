#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
	exit 0
fi

echo "invalid release version: $version (expected vMAJOR.MINOR.PATCH without leading zeroes)" >&2
exit 1
