#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/dist" "$tmp/home/.codex" "$tmp/home/.claude"
: > "$tmp/home/.zshrc"
go build -trimpath -o "$tmp/dist/greenrun" "$root/cmd/greenrun"
cp "$root/skills/greenrun/SKILL.md" "$tmp/dist/SKILL.md"

HOME="$tmp/home" \
GREENRUN_INSTALL_DIR="$tmp/home/.greenrun" \
GREENRUN_LOCAL_DIST="$tmp/dist" \
sh "$root/install.sh"

HOME="$tmp/home" \
GREENRUN_INSTALL_DIR="$tmp/home/.greenrun" \
GREENRUN_LOCAL_DIST="$tmp/dist" \
sh "$root/install.sh"

"$tmp/home/.greenrun/bin/greenrun" version >/dev/null
test -f "$tmp/home/.codex/skills/greenrun/SKILL.md"
test -f "$tmp/home/.claude/skills/greenrun/SKILL.md"
test "$(grep -c "$tmp/home/.greenrun/bin" "$tmp/home/.zshrc")" -eq 1

mkdir -p "$tmp/release"
case "$(uname -s)" in Darwin) os=darwin ;; *) os=linux ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; *) arch=x64 ;; esac
asset="greenrun-${os}-${arch}.tar.gz"
tar -czf "$tmp/release/$asset" -C "$tmp/dist" greenrun SKILL.md
printf '%064d  %s\n' 0 "$asset" > "$tmp/release/SHA256SUMS"
if HOME="$tmp/home" \
  GREENRUN_INSTALL_DIR="$tmp/home/bad" \
  GREENRUN_RELEASE_BASE="file://$tmp/release" \
  sh "$root/install.sh" >/dev/null 2>&1; then
  echo "installer accepted a bad checksum" >&2
  exit 1
fi
