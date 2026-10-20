#!/bin/sh
# Install the ego binary from a GitHub release.
#   curl -fsSL https://raw.githubusercontent.com/triageagent-dev/ego/main/install.sh | sh
#   EGO_VERSION=v0.1.0 EGO_INSTALL_DIR=$HOME/.local/bin ./install.sh
set -eu

repo="triageagent-dev/ego"
version="${EGO_VERSION:-latest}"
dir="${EGO_INSTALL_DIR:-/usr/local/bin}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in linux|darwin) ;; *) echo "unsupported OS: $os" >&2; exit 1 ;; esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ "$version" = latest ]; then
  version="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$version" ] || { echo "could not find the latest release" >&2; exit 1; }
fi

name="ego_${version#v}_${os}_${arch}"
base="https://github.com/$repo/releases/download/$version"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ego $version for $os/$arch"
curl -fsSL -o "$tmp/$name.tar.gz" "$base/$name.tar.gz"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

want="$(grep " $name.tar.gz\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$tmp/$name.tar.gz" | cut -d' ' -f1)"; else got="$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d' ' -f1)"; fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "checksum mismatch" >&2; exit 1; }

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
if [ -w "$dir" ]; then install -m 0755 "$tmp/$name/ego" "$dir/ego"; else sudo install -m 0755 "$tmp/$name/ego" "$dir/ego"; fi
echo "Installed $dir/ego"
"$dir/ego" version
