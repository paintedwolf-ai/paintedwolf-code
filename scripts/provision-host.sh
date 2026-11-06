#!/usr/bin/env bash
# Prepare a fresh macOS Apple Silicon host for ./task development runs.
#
# Operator prerequisites: GitHub SSH access, passwordless sudo, and gh
# authentication (gh auth login). Everything else is handled here.
#
# Idempotent: safe to rerun; each step skips work that is already done.
# Overrides: REPO_SLUG (default paintedwolf-ai/paintedwolf-code) and
# REPO_DIR (default ~/git/github/paintedwolf-code).
set -euo pipefail

REPO_SLUG="${REPO_SLUG:-paintedwolf-ai/paintedwolf-code}"
REPO_DIR="${REPO_DIR:-${HOME}/git/github/paintedwolf-code}"
REPO_DIR="${REPO_DIR/#\~/$HOME}"

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
	echo "error: host provisioning supports macOS on Apple Silicon" >&2
	exit 1
fi

if [[ -x /opt/homebrew/bin/brew ]]; then
	echo "==> Homebrew already installed"
else
	if ! sudo -n true 2>/dev/null; then
		echo "error: passwordless sudo is required to install Homebrew — configure it, then re-run this script" >&2
		exit 1
	fi
	echo "==> Installing Homebrew"
	/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
fi

eval "$(/opt/homebrew/bin/brew shellenv zsh)"
export PATH="/opt/homebrew/opt/rustup/bin:${PATH}"

# Non-interactive shells (ssh command mode, automation) never read ~/.zprofile,
# so the PATH setup belongs in ~/.zshenv. The rustup formula is keg-only and
# needs its opt prefix on PATH for rustup and cargo to resolve.
zshenv="${HOME}/.zshenv"
touch "${zshenv}"
# shellcheck disable=SC2016  # the lines must land in ~/.zshenv unexpanded
brew_line='eval "$(/opt/homebrew/bin/brew shellenv zsh)"'
# shellcheck disable=SC2016
rustup_line='export PATH=/opt/homebrew/opt/rustup/bin:$PATH'
grep -qxF "${brew_line}" "${zshenv}" || printf '%s\n' "${brew_line}" >> "${zshenv}"
grep -qxF "${rustup_line}" "${zshenv}" || printf '%s\n' "${rustup_line}" >> "${zshenv}"

formulae=(go node bun rustup direnv python gh cmake)
missing=()
for formula in "${formulae[@]}"; do
	brew list --formula "${formula}" >/dev/null 2>&1 || missing+=("${formula}")
done
if ((${#missing[@]} > 0)); then
	echo "==> Installing missing Homebrew formulae: ${missing[*]}"
	HOMEBREW_NO_AUTO_UPDATE=1 brew install "${missing[@]}"
else
	echo "==> Homebrew formulae already installed"
fi

echo "==> Preparing GitHub access"
mkdir -p "${HOME}/.ssh"
chmod 700 "${HOME}/.ssh"
touch "${HOME}/.ssh/known_hosts"
if ! ssh-keygen -F github.com -f "${HOME}/.ssh/known_hosts" >/dev/null 2>&1; then
	ssh-keyscan -t ed25519 github.com >> "${HOME}/.ssh/known_hosts" || {
		echo "error: could not record github.com's host key; check network access" >&2
		exit 1
	}
fi
if ! gh auth status >/dev/null 2>&1; then
	echo "error: gh is not authenticated — run: gh auth login, then re-run this script" >&2
	exit 1
fi
# GitHub's sshd exits 1 on successful `ssh -T` authentication; 255 means the
# SSH key prerequisite is missing.
ssh_rc=0
# -n keeps the probe from consuming the script's own stdin under `bash -s`.
ssh -n -o BatchMode=yes -o ConnectTimeout=10 -T git@github.com >/dev/null 2>&1 || ssh_rc=$?
if [[ "${ssh_rc}" != 0 && "${ssh_rc}" != 1 ]]; then
	echo "error: SSH authentication to github.com failed — set up GitHub SSH access, then re-run this script" >&2
	exit 1
fi
if [[ -d "${REPO_DIR}" ]]; then
	echo "==> Repository already present at ${REPO_DIR}"
else
	echo "==> Cloning ${REPO_SLUG} into ${REPO_DIR}"
	gh repo clone "${REPO_SLUG}" "${REPO_DIR}" || {
		echo "error: cloning ${REPO_SLUG} failed — check GitHub SSH access and gh authentication, then re-run this script" >&2
		exit 1
	}
fi

channel="$(sed -n 's/^channel = "\(.*\)"/\1/p' "${REPO_DIR}/rust-toolchain.toml")"
if [[ -z "${channel}" ]]; then
	echo "error: could not parse channel from ${REPO_DIR}/rust-toolchain.toml" >&2
	exit 1
fi
if rustup toolchain list | grep -q "^${channel}"; then
	echo "==> Rust ${channel} already installed"
else
	echo "==> Installing Rust ${channel}"
	rustup toolchain install "${channel}" --profile minimal
fi

# setup-dev's golangci bootstrap validates workflows through the Python tools,
# which only the --ci-inputs provision prepares; plain setup-dev fails on a
# fresh machine until that provision has run.
echo "==> Preparing pinned verification inputs"
(cd "${REPO_DIR}" && ./task setup-dev -- --ci-inputs)

echo "==> Running setup-dev"
(cd "${REPO_DIR}" && ./task setup-dev)

echo ""
echo "Host provisioning complete."
