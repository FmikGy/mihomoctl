#!/bin/sh
set -eu

if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

if command -v mihomoctl >/dev/null 2>&1; then
	mihomoctl config sync-public-state >/dev/null 2>&1 || true
fi

exit 0
