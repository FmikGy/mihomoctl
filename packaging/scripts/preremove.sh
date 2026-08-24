#!/bin/sh
set -eu

# Debian passes "remove" for an uninstall; RPM passes 0. Do not disable a
# user's timer during an in-place package upgrade.
case "${1:-}" in
	remove|0)
		if command -v systemctl >/dev/null 2>&1; then
			systemctl disable --now mihomoctl-update.timer >/dev/null 2>&1 || true
			systemctl stop mihomoctl-update.service >/dev/null 2>&1 || true
		fi
		;;
esac

exit 0
