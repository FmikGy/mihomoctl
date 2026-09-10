#!/bin/sh
set -eu

if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

if [ -x /usr/bin/mihomoctl ]; then
	/usr/bin/mihomoctl config sync-public-state >/dev/null 2>&1 || true
fi

exit 0
