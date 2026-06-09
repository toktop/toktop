#!/bin/sh
# toktop installer — downloads the latest prebuilt binary for your platform.
#
#   curl -fsSL https://toktop.unceas.dev/install.sh | sh
#
# Override the install directory with TOKTOP_INSTALL_DIR (default ~/.local/bin).
set -eu

REPO="toktop/toktop"
INSTALL_DIR="${TOKTOP_INSTALL_DIR:-$HOME/.local/bin}"
BASE="https://github.com/${REPO}/releases/latest/download"

err() { echo "toktop-install: $*" >&2; exit 1; }

os=$(uname -s)
case "$os" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) err "unsupported OS '$os' — on Windows use install.ps1" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) err "unsupported architecture '$arch'" ;;
esac

asset="toktop_${os}_${arch}.tar.gz"

if command -v curl >/dev/null 2>&1; then
	dl() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	dl() { wget -qO "$2" "$1"; }
else
	err "need curl or wget"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${asset}…"
dl "${BASE}/${asset}" "$tmp/$asset" || err "download failed: ${BASE}/${asset}"
dl "${BASE}/checksums.txt" "$tmp/checksums.txt" || err "checksum download failed"

# Optional signature check: when cosign is installed, verify that checksums.txt was
# signed (keyless/sigstore) by toktop's release workflow — so a tampered release is
# caught, not just transit corruption. Without cosign the sha256 check below still runs.
if command -v cosign >/dev/null 2>&1; then
	if dl "${BASE}/checksums.txt.bundle" "$tmp/checksums.txt.bundle" 2>/dev/null; then
		echo "Verifying signature…"
		cosign verify-blob \
			--bundle "$tmp/checksums.txt.bundle" \
			--certificate-identity-regexp '^https://github\.com/toktop/toktop/\.github/workflows/release\.yml@refs/tags/' \
			--certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
			"$tmp/checksums.txt" >/dev/null 2>&1 || err "signature verification failed"
	else
		echo "toktop-install: no signature for this release — skipping (sha256 still verified)" >&2
	fi
else
	echo "toktop-install: cosign not installed — skipping signature verification (sha256 still verified)" >&2
fi

echo "Verifying checksum…"
expected=$(awk -v f="$asset" '$2 == f {print $1}' "$tmp/checksums.txt")
[ -n "$expected" ] || err "no checksum listed for $asset"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
else
	err "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || err "checksum mismatch (expected $expected, got $actual)"

tar -xzf "$tmp/$asset" -C "$tmp"
[ -f "$tmp/toktop" ] || err "archive did not contain a toktop binary"
mkdir -p "$INSTALL_DIR"
mv "$tmp/toktop" "$INSTALL_DIR/toktop"
chmod +x "$INSTALL_DIR/toktop"

echo "Installed toktop to $INSTALL_DIR/toktop"
case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) echo "Note: $INSTALL_DIR is not on your PATH — add: export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
echo "Run 'toktop --help' to get started."
