import type { UpdateError } from "./update-error.ts";
import { invoke } from "@tauri-apps/api/core";
import { listenHostEvent } from "../../platform/windows/window-channel.ts";

export const UPDATE_STATE_EVENT = "update-state-changed";

export type InstallSource = "homebrew_cask" | "direct_download" | "unknown";
export type UpdateChannel = "stable" | "preview";
export type UpdatePhase =
  | "idle"
  | "checking"
  | "up_to_date"
  | "available"
  | "held_back"
  | "installing"
  | "restart_required"
  | "unavailable";

export type NativeUpdateState = {
  revision: number;
  phase: UpdatePhase;
  current_version: string;
  channel: UpdateChannel;
  rollout_eligibility: "not_applicable" | "eligible" | "held_back";
  available_version?: string;
  notes?: string;
  install_source: InstallSource;
  checks_enabled: boolean;
  downloaded_bytes: number;
  total_bytes: number | null;
  error?: UpdateError;
};

export type UpdateService = {
  getState: () => Promise<NativeUpdateState>;
  setChecksEnabled: (enabled: boolean) => Promise<NativeUpdateState>;
  setChannel: (channel: UpdateChannel) => Promise<NativeUpdateState>;
  check: () => Promise<NativeUpdateState>;
  install: (expectedVersion: string) => Promise<NativeUpdateState>;
  subscribe: (
    handler: (state: NativeUpdateState) => void,
  ) => Promise<() => void>;
};

export const nativeUpdateService: UpdateService = {
  getState: () => invoke<NativeUpdateState>("get_update_state"),
  setChecksEnabled: (enabled) =>
    invoke<NativeUpdateState>("set_update_checks_enabled", { enabled }),
  setChannel: (channel) =>
    invoke<NativeUpdateState>("set_update_channel", { channel }),
  check: () => invoke<NativeUpdateState>("check_update"),
  install: (expectedVersion) =>
    invoke<NativeUpdateState>("install_update", { expectedVersion }),
  subscribe: (handler) =>
    listenHostEvent<NativeUpdateState>(UPDATE_STATE_EVENT, (event) => {
      handler(event.payload);
    }),
};
