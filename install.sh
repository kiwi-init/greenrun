#!/bin/sh
set -eu

REPO="kiwi-init/greenrun"
INSTALL_DIR="${GREENRUN_INSTALL_DIR:-$HOME/.greenrun}"
BIN_DIR="$INSTALL_DIR/bin"
VERSION="${GREENRUN_VERSION:-latest}"

say() { printf '%s\n' "greenrun $*"; }
die() { printf '%s\n' "greenrun: $*" >&2; exit 1; }

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else return 1
  fi
}

command -v git >/dev/null 2>&1 || die "git is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS: $os (use WSL on Windows)" ;;
esac
case "$arch" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64) arch=x64 ;;
  *) die "unsupported architecture: $arch" ;;
esac

asset="greenrun-${os}-${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$BIN_DIR"

if [ -n "${GREENRUN_LOCAL_DIST:-}" ]; then
  cp "$GREENRUN_LOCAL_DIST/greenrun" "$tmp/greenrun"
  cp "$GREENRUN_LOCAL_DIST/SKILL.md" "$tmp/SKILL.md"
else
  if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
  elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
  else
    die "curl or wget is required"
  fi

  if [ -n "${GREENRUN_RELEASE_BASE:-}" ]; then
    base="$GREENRUN_RELEASE_BASE"
  elif [ "$VERSION" = latest ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/$VERSION"
  fi
  say "downloading $asset"
  fetch "$base/$asset" "$tmp/$asset" || die "release download failed"
  fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || die "checksum manifest download failed"
  actual=$(sha256_file "$tmp/$asset") || die "sha256sum or shasum is required"
  expected=$(awk -v n="$asset" '$2 == n || $2 == "*" n {print $1; exit}' "$tmp/SHA256SUMS")
  [ -n "$expected" ] && [ "$actual" = "$expected" ] || die "checksum mismatch"
  tar -xzf "$tmp/$asset" -C "$tmp"
fi

install -m 0755 "$tmp/greenrun" "$BIN_DIR/greenrun"
printf '%s\n' "$VERSION" > "$INSTALL_DIR/version"

if [ -z "${GREENRUN_NO_WIRE:-}" ] && [ -f "$tmp/SKILL.md" ]; then
  if command -v codex >/dev/null 2>&1 || [ -d "$HOME/.codex" ]; then
    mkdir -p "$HOME/.codex/skills/greenrun"
    install -m 0644 "$tmp/SKILL.md" "$HOME/.codex/skills/greenrun/SKILL.md"
    say "installed Codex skill"
  fi
  if command -v claude >/dev/null 2>&1 || [ -d "$HOME/.claude" ]; then
    mkdir -p "$HOME/.claude/skills/greenrun"
    install -m 0644 "$tmp/SKILL.md" "$HOME/.claude/skills/greenrun/SKILL.md"
    say "installed Claude Code skill"
  fi
fi

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    if [ -z "${GREENRUN_NO_MODIFY_PATH:-}" ]; then
      line="export PATH=\"$BIN_DIR:\$PATH\""
      for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
        [ -f "$rc" ] || continue
        grep -qF "$BIN_DIR" "$rc" 2>/dev/null || printf '\n# Greenrun\n%s\n' "$line" >> "$rc"
      done
      say "added $BIN_DIR to shell PATH"
    fi
    ;;
esac

say "installed to $BIN_DIR/greenrun"
