import type { UpdateError } from "./update-error.ts";
import { invoke } from "@tauri-apps/api/core";
import { listenHostEvent } from "../../platform/windows/window-channel.ts";
const UPDATE_STATE_EVENT = "update-state-changed";
type InstallSource = "homebrew_cask" | "direct_download" | "unknown";
type UpdateChannel = "stable" | "preview";
type UpdateCandidate = {
  release_id: string; version: string; channel: UpdateChannel; platform: string;
  signing_generation: number; artifact_url: string; artifact_signature: string;
  notes: string | null; rollout_eligibility: "eligible" | "held_back";
};
type UpdateInstallation =
  | "none" | "downloading" | "verifying" | "preparing" | "staged"
  | "awaiting_exit" | "committed" | "awaiting_startup" | "recovery_required" | "failed";
export type NativeUpdateState = {
  service_instance_id: string; revision: number; running_version: string; channel: UpdateChannel;
  startup_pending: boolean; automatic_updates_enabled: boolean; install_source: InstallSource;
  capabilities: { can_check: boolean; can_download: boolean; can_restart_to_update: boolean; can_install_automatically: boolean; blocked_reason: string | null };
  discovery: "idle" | "checking" | "up_to_date" | "available" | "held_back" | "failed";
  candidate: UpdateCandidate | null;
  installation: UpdateInstallation;
  staged_release_id: string | null; downloaded_bytes: number; total_bytes: number | null;
  last_check_at: number | null; next_check_at: number | null; offer_confirmed_at: number | null;
  last_error: UpdateError | null;
};
export type UpdateService = {
  getState: () => Promise<NativeUpdateState>;
  setAutomaticUpdatesEnabled: (enabled: boolean) => Promise<NativeUpdateState>;
  setChannel: (channel: UpdateChannel) => Promise<NativeUpdateState>;
  check: () => Promise<NativeUpdateState>;
  download: (expectedReleaseId: string) => Promise<NativeUpdateState>;
  retry: (expectedReleaseId: string) => Promise<NativeUpdateState>;
  restart: (expectedReleaseId: string) => Promise<void>;
  subscribe: (handler: (state: NativeUpdateState) => void) => Promise<() => void>;
};
export const nativeUpdateService: UpdateService = {
  getState: () => invoke("get_update_state"),
  setAutomaticUpdatesEnabled: (enabled) => invoke("set_automatic_updates_enabled", { enabled }),
  setChannel: (channel) => invoke("set_update_channel", { channel }),
  check: () => invoke("check_update"),
  download: (expectedReleaseId) => invoke("download_update", { expectedReleaseId }),
  retry: (expectedReleaseId) => invoke("retry_update", { expectedReleaseId }),
  restart: (expectedReleaseId) => invoke("restart_to_update", { expectedReleaseId }),
  subscribe: (handler) => listenHostEvent<NativeUpdateState>(UPDATE_STATE_EVENT, ({ payload }) => handler(payload)),
};
