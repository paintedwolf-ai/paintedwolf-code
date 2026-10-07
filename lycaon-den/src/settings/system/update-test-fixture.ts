import type { NativeUpdateState, UpdateService } from "./update-service.ts";
export const updateFixture = (over: Partial<NativeUpdateState> = {}): NativeUpdateState => ({
  service_instance_id: "native-1", revision: 1, running_version: "1.0.1", channel: "stable", automatic_updates_enabled: true,
  install_source: "direct_download", capabilities: { can_check: true, can_download: true, can_restart_to_update: false, can_install_automatically: false, blocked_reason: null },
  discovery: "available", candidate: { release_id: "release-1", version: "1.1.0", channel: "stable", platform: "darwin-aarch64", signing_generation: 1, artifact_url: "https://downloads.paintedwolf.dev/app", artifact_signature: "signature", notes: "A new release.", rollout_eligibility: "eligible" },
  startup_pending: false, installation: "none", staged_release_id: null, downloaded_bytes: 0, total_bytes: null, last_check_at: null, next_check_at: null, offer_confirmed_at: null, last_error: null, ...over,
});
export const updateServiceFixture = (over: Partial<UpdateService> = {}): UpdateService => ({
  getState: async () => updateFixture(), setAutomaticUpdatesEnabled: async (enabled) => updateFixture({ automatic_updates_enabled: enabled }),
  setChannel: async (channel) => updateFixture({ channel }), check: async () => updateFixture(), download: async () => updateFixture(),
  retry: async () => updateFixture(), restart: async () => {}, subscribe: async () => () => {}, ...over,
});
export const stagedFixture = (): NativeUpdateState => updateFixture({ installation: "staged", staged_release_id: "release-1", capabilities: { can_check: true, can_download: false, can_restart_to_update: true, can_install_automatically: true, blocked_reason: null } });
