#!/usr/bin/env bash
set -euo pipefail

prefix="${PREFIX:-/usr}"
destdir="${DESTDIR:-}"
go_bin="${GO_BIN:-go}"
install_units=1

usage() {
	cat <<'EOF'
Usage: scripts/install.sh [options]

Build and install mihomoctl from the current source tree.

Options:
  --prefix PATH     Installation prefix (default: /usr)
  --destdir PATH    Staging root used by package builders
  --go PATH         Go executable (default: go)
  --no-units        Do not install systemd update units
  -h, --help        Show this help

The update timer is installed in a disabled state. After initialization, enable it with:
  mihomoctl schedule enable
EOF
}

while (($# > 0)); do
	case "$1" in
		--prefix)
			[[ $# -ge 2 ]] || { echo "--prefix requires a path" >&2; exit 2; }
			prefix="$2"
			shift 2
			;;
		--destdir)
			[[ $# -ge 2 ]] || { echo "--destdir requires a path" >&2; exit 2; }
			destdir="$2"
			shift 2
			;;
		--go)
			[[ $# -ge 2 ]] || { echo "--go requires an executable" >&2; exit 2; }
			go_bin="$2"
			shift 2
			;;
		--no-units)
			install_units=0
			shift
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			echo "unknown option: $1" >&2
			usage >&2
			exit 2
			;;
	esac
done

[[ "$prefix" == /* ]] || { echo "prefix must be an absolute path" >&2; exit 2; }
if [[ -n "$destdir" && "$destdir" != /* ]]; then
	echo "destdir must be an absolute path" >&2
	exit 2
fi
if ((install_units)) && [[ "$prefix" != "/usr" && "$prefix" != "/usr/local" ]]; then
	echo "systemd units require --prefix /usr or /usr/local; use --no-units with a custom prefix" >&2
	exit 2
fi

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
build_dir="$(mktemp -d -t mihomoctl-install.XXXXXXXX)"
cleanup() {
	rm -rf -- "$build_dir"
}
trap cleanup EXIT

version="$(git -C "$repo_dir" describe --tags --always --dirty 2>/dev/null || printf 'dev')"
commit="$(git -C "$repo_dir" rev-parse --short HEAD 2>/dev/null || printf 'none')"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w -X mihomoctl/internal/domain.Version=$version -X mihomoctl/internal/domain.Commit=$commit -X mihomoctl/internal/domain.Date=$build_date"

(
	cd "$repo_dir"
	CGO_ENABLED=0 "$go_bin" build -trimpath -ldflags "$ldflags" -o "$build_dir/mihomoctl" ./cmd/mihomoctl
)

install_prefix=()
if [[ -z "$destdir" && "$(id -u)" -ne 0 ]]; then
	command -v sudo >/dev/null 2>&1 || {
		echo "installation requires root or sudo" >&2
		exit 3
	}
	install_prefix=(sudo --)
fi

bindir="$destdir$prefix/bin"
unitdir="$destdir$prefix/lib/systemd/system"
configdir="$destdir/etc/mihomoctl"
statedir="$destdir/var/lib/mihomoctl"
docdir="$destdir$prefix/share/doc/mihomoctl"
"${install_prefix[@]}" install -d -m 0755 "$bindir"
"${install_prefix[@]}" install -m 0755 "$build_dir/mihomoctl" "$bindir/mihomoctl"
"${install_prefix[@]}" install -d -m 0700 "$configdir"
"${install_prefix[@]}" install -d -m 0755 "$statedir"
"${install_prefix[@]}" install -d -m 0755 "$docdir"
"${install_prefix[@]}" install -m 0644 "$repo_dir/LICENSE" "$repo_dir/README.md" "$docdir/"

if ((install_units)); then
	"${install_prefix[@]}" install -d -m 0755 "$unitdir"
	"${install_prefix[@]}" install -m 0644 "$repo_dir/packaging/systemd/mihomoctl-update.service" "$unitdir/mihomoctl-update.service"
	"${install_prefix[@]}" install -m 0644 "$repo_dir/packaging/systemd/mihomoctl-update.timer" "$unitdir/mihomoctl-update.timer"
	if [[ -z "$destdir" ]] && command -v systemctl >/dev/null 2>&1; then
		"${install_prefix[@]}" systemctl daemon-reload
	fi
fi

if [[ -z "$destdir" ]]; then
	"${install_prefix[@]}" "$bindir/mihomoctl" config sync-public-state >/dev/null 2>&1 || true
fi

echo "installed $bindir/mihomoctl"
if ((install_units)); then
	echo "installed disabled timer; after init, run 'mihomoctl schedule enable' to opt in"
fi
