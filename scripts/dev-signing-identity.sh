#!/usr/bin/env bash
# Keeps the development signing identity stable across rebuilds.
# Its requirement binds the identifier to this machine's certificate.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=dev-signing-config.sh
source "${SCRIPT_DIR}/dev-signing-config.sh"

if [[ "$(uname -s)" != "Darwin" ]]; then
  exit 0
fi

VALID_DAYS=3650

login_keychain() {
  security default-keychain -d user | sed -e 's/^[[:space:]]*"//' -e 's/"$//'
}

identity_is_valid() {
  security find-identity -v -p codesigning 2>/dev/null | grep -qF "${DEV_SIGNING_IDENTITY}"
}

remove_identity() {
  local pem
  pem="$(mktemp)"
  if security find-certificate -c "${DEV_SIGNING_IDENTITY}" -p > "${pem}" 2>/dev/null; then
    echo "macOS will ask for your password to withdraw trust." >&2
    security remove-trusted-cert "${pem}" 2>/dev/null || true
  fi
  rm -f "${pem}"
  security delete-identity -c "${DEV_SIGNING_IDENTITY}" "$(login_keychain)" >/dev/null 2>&1 || true
  echo "dev signing identity removed" >&2
}

if [[ "${1:-}" == "--remove" ]]; then
  remove_identity
  exit 0
fi

if identity_is_valid; then
  echo "dev signing identity present: ${DEV_SIGNING_IDENTITY}" >&2
  exit 0
fi

cat >&2 <<'NOTE'
==> Provisioning a development code-signing identity

This is optional. The development credential vault needs no signing identity.
Provision one only for development integrations that read Keychain, such as the
Git credential helper.

macOS will prompt for your login password twice during this setup:
  1. to trust the new certificate for code signing
  2. the first time codesign uses its private key — choose "Always Allow"

Both authorize a self-signed certificate that exists only on this machine and
signs nothing that leaves it. Undo with: ./task dev:signing-identity -- --remove
NOTE

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

cat > "${WORK}/req.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = v3
prompt = no

[dn]
CN = ${DEV_SIGNING_IDENTITY}

[v3]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
subjectKeyIdentifier = hash
EOF

echo "==> Generating certificate" >&2
openssl req -x509 -newkey rsa:2048 -nodes -days "${VALID_DAYS}" \
  -keyout "${WORK}/key.pem" -out "${WORK}/cert.pem" \
  -config "${WORK}/req.cnf" >/dev/null 2>&1

P12_PW="$(openssl rand -base64 24)"
openssl pkcs12 -export -out "${WORK}/identity.p12" \
  -inkey "${WORK}/key.pem" -in "${WORK}/cert.pem" \
  -name "${DEV_SIGNING_IDENTITY}" -passout "pass:${P12_PW}" >/dev/null 2>&1

echo "==> Importing into the login keychain" >&2
# Allow repeated signing with this key.
security import "${WORK}/identity.p12" -P "${P12_PW}" \
  -T /usr/bin/codesign -f pkcs12 -k "$(login_keychain)" >/dev/null

echo "==> Trusting the certificate for code signing (password prompt 1 of 2)" >&2
if ! security add-trusted-cert -r trustRoot -p codeSign "${WORK}/cert.pem"; then
  echo "warning: trust was not granted — the identity is unusable." >&2
  echo "         Re-run ./task dev:signing-identity, or continue unsigned." >&2
  exit 0
fi

if identity_is_valid; then
  echo "dev signing identity ready: ${DEV_SIGNING_IDENTITY}" >&2
  echo "  ./task build:lycaon-dev signs the engine with it from now on" >&2
  echo "  the next build triggers password prompt 2 of 2 — choose Always Allow" >&2
else
  echo "warning: ${DEV_SIGNING_IDENTITY} is still not a valid signing identity" >&2
fi
