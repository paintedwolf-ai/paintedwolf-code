#!/usr/bin/env bash
# Resolve the host configuration root.
# shellcheck shell=bash

# Host configuration directory names.
CONFIG_DIR_NAME_PROD="${CONFIG_DIR_NAME_PROD:-paintedwolf}"
CONFIG_DIR_NAME_DEV="${CONFIG_DIR_NAME_DEV:-paintedwolf-dev}"

# lycaon_config_dir_leaf — XDG segment for the current channel.
lycaon_config_dir_leaf() {
  if [[ "${LYCAON_DEV:-}" == "1" || "${LYCAON_DEV:-}" == "true" || "${LYCAON_DEV:-}" == "TRUE" || "${LYCAON_DEV:-}" == "yes" ]]; then
    printf '%s\n' "${CONFIG_DIR_NAME_DEV}"
  else
    printf '%s\n' "${CONFIG_DIR_NAME_PROD}"
  fi
}

# lycaon_overlay_dir returns the shared project overlay name.
lycaon_overlay_dir() {
  printf '%s\n' '.paintedwolf'
}

# OVERLAY_FIXTURE_DIR names project overlays in fixtures.
OVERLAY_FIXTURE_DIR="${OVERLAY_FIXTURE_DIR:-overlay-dir}"

# lycaon_channel_config_dir ignores test overrides.
lycaon_channel_config_dir() {
  local home="${HOME:-}"
  if [[ -z "${home}" ]]; then
    home="${USERPROFILE:-}"
  fi
  printf '%s\n' "${home}/.config/$(lycaon_config_dir_leaf)"
}

# lycaon_config_dir returns the resolved configuration root.
lycaon_config_dir() {
  if [[ -n "${LYCAON_CONFIG_DIR:-}" ]]; then
    printf '%s\n' "${LYCAON_CONFIG_DIR}"
    return
  fi
  lycaon_channel_config_dir
}
