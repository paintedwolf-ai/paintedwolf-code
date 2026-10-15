# shellcheck shell=bash
# SHA-256 digests with whichever tool the platform ships: sha256sum (Linux) or
# shasum (macOS; on Arch it is outside the default PATH). Both print
# "<hex>  <name>" lines, so combined digests agree across platforms.

# sha256 [FILE...] digests the files, or stdin when none are named.
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    shasum -a 256 "$@"
  fi
}

# sha256_hex [FILE] prints only the hex digest of one file or of stdin.
sha256_hex() {
  sha256 "$@" | awk '{print $1}'
}
