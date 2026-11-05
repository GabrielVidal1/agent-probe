#!/bin/sh
# Install agent-probe: download the release binary for this machine, check it
# against the release's checksums.txt, put it in ~/.local/bin, then run
# `agent-probe install` (the hooks / plugins of every coding agent found:
# Claude Code, opencode, Codex, pi).
#
#   curl -fsSL https://raw.githubusercontent.com/GabrielVidal1/agent-probe/main/install.sh | sh
#   … | sh -s -- --statusline     # also show the last call's cost in Claude Code's status line
#   … | sh -s -- --agent codex    # only some agents
#   … | sh -s -- --no-setup       # the binary only
#
# On Linux, exact mode (every process, from the kernel) needs one more step,
# printed at the end: sudo agent-probe setup-kernel.
#
# Environment: AGENT_PROBE_VERSION (a tag, default: latest),
# AGENT_PROBE_BIN_DIR (default ~/.local/bin).
set -eu

REPO="https://github.com/GabrielVidal1/agent-probe"
BIN_DIR="${AGENT_PROBE_BIN_DIR:-$HOME/.local/bin}"
SETUP=1
for a in "$@"; do
  case "$a" in --no-setup) SETUP=0 ;; esac
done

say() { printf '%s\n' "$*" >&2; }
die() { say "agent-probe install: $*"; exit 1; }
command -v curl >/dev/null 2>&1 || die "needs curl"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "no build for $(uname -s) — go install github.com/GabrielVidal1/agent-probe/cmd/agent-probe@latest" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "no build for $(uname -m)" ;;
esac
asset="agent-probe-${os}-${arch}"

tag="${AGENT_PROBE_VERSION:-}"
if [ -z "$tag" ]; then
  # /releases/latest redirects to /releases/tag/<tag>: no API, no rate limit.
  loc="$(curl -fsSI "$REPO/releases/latest" | tr -d '\r' | sed -n 's/^[Ll]ocation: *//p' | tail -n1)"
  tag="${loc##*/}"
  case "$tag" in v[0-9]*) ;; *) die "couldn't find the latest release at $REPO/releases" ;; esac
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
say "Downloading agent-probe $tag ($os/$arch)…"
curl -fsSL "$REPO/releases/download/$tag/$asset" -o "$tmp/agent-probe" || die "download failed: $asset"
curl -fsSL "$REPO/releases/download/$tag/checksums.txt" -o "$tmp/checksums.txt" || die "download failed: checksums.txt"
want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "$tmp/checksums.txt")"
[ -n "$want" ] || die "$asset is not in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/agent-probe" | awk '{print $1}')"
else
  got="$(shasum -a 256 "$tmp/agent-probe" | awk '{print $1}')"
fi
[ "$got" = "$want" ] || die "checksum mismatch for $asset"
chmod 755 "$tmp/agent-probe"
"$tmp/agent-probe" version >/dev/null 2>&1 || die "the downloaded binary doesn't run here"

mkdir -p "$BIN_DIR"
mv -f "$tmp/agent-probe" "$BIN_DIR/agent-probe"
say "Installed $BIN_DIR/agent-probe ($tag)"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say "Note: $BIN_DIR is not on your PATH." ;;
esac

if [ "$SETUP" = 1 ]; then
  n=$#
  i=0
  while [ "$i" -lt "$n" ]; do
    a="$1"
    shift
    case "$a" in --no-setup) ;; *) set -- "$@" "$a" ;; esac
    i=$((i + 1))
  done
  exec "$BIN_DIR/agent-probe" install "$@"
fi
