import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { Show, onCleanup, onMount } from "solid-js";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { MarkdownBody } from "../transcript/MarkdownBody.tsx";
import { settingAnchor, settingLabel } from "../../settings/settings-registry.ts";
import { createUpdateState, nativeUpdateState } from "../../settings/system/update-state.ts";
import { UPDATE_ERROR_MESSAGES } from "../../settings/system/update-error.ts";
import type { NativeUpdateState, UpdateService } from "../../settings/system/update-service.ts";
export type UpdatePanelProps = { preferences?: boolean; updateService?: UpdateService };

/** One line describing where the installation stands; empty when nothing is in progress. */
function installationStatus(state: NativeUpdateState): string {
  switch (state.installation) {
    case "downloading": return "Downloading…";
    case "verifying":
    case "preparing": return "Preparing update…";
    case "staged": return state.offer_confirmed_at === null ? "Update downloaded. Check for updates again before installing." : state.automatic_updates_enabled ? "Update downloaded. It will install when you quit after a final safety check." : "Update downloaded. Restart to install it.";
    case "awaiting_exit": return "Finishing your work before the update installs…";
    case "committed": return "The update is committed and is waiting for running copies of this installation to quit. Reopen this copy if the update remains pending.";
    case "awaiting_startup": return "The update is installed. The previous application is retained until the engine finishes starting successfully.";
    case "recovery_required": return "The update handoff was committed but could not be recorded.";
    default: return "";
  }
}

export function UpdatePanel(props: UpdatePanelProps) {
  const updates = props.updateService ? createUpdateState(props.updateService) : nativeUpdateState;
  onMount(() => onCleanup(updates.mount()));
  const state = updates.state;
  usePresentationParticipant("update-state", () => state() !== null || updates.error() !== null);
  const candidate = () => state()?.candidate;
  const nix = () => state()?.package?.family === "nix";
  const locked = () => state()?.capabilities.blocked_reason === "development_build" || updates.busy() || !state() || state()?.installation === "recovery_required";
  const progress = () => {
    const current = state();
    const total = current?.total_bytes;
    return current && total ? ` ${Math.min(100, Math.round(current.downloaded_bytes / total * 100))}%` : "";
  };
  const action = () => {
    if (state()?.capabilities.can_restart_to_update) { void updates.restart(); return; }
    const release = candidate();
    if (!release) return;
    void updates.run(() => state()?.installation === "failed" ? updates.service.retry(release.release_id) : updates.service.download(release.release_id));
  };
  return <div class="den-settings-section" data-testid="updates-settings-panel">
    <Show when={props.preferences}>
      <div class="den-settings-pref-group">
        <div class="den-settings-pref-row" {...settingAnchor("update-checks")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("update-checks")}</span>
            <Show when={state()?.install_source === "system_package" || state()?.install_source === "homebrew_cask"} fallback={<p class="den-settings-hint">Downloads updates in the background and installs them when you quit or next open Painted Wolf Code. The app never restarts automatically while you’re using it.</p>}>
              <p class="den-settings-hint" data-testid="updates-package-hint">{state()?.install_source === "homebrew_cask" ? "Checks for updates in the background and tells you when a newer package is available. Updates install through Homebrew, never from inside the app." : nix() ? "Checks upstream releases in the background. The Nix configuration or profile that selected this build controls its version; this check does not change its input or pin." : "Checks upstream releases in the background. Your system package manager controls which versions your configured sources can install."}</p>
            </Show>
          </div>
          <DenCheckbox checked={state()?.automatic_updates_enabled ?? false} disabled={locked()} data-testid="updates-check-enabled" onChange={(event) => { const enabled = event.currentTarget.checked; void updates.run(() => updates.service.setAutomaticUpdatesEnabled(enabled)); }}><span class="sr-only">Automatic updates</span></DenCheckbox>
        </div>
        <div class="den-settings-pref-row" {...settingAnchor("release-channel")}>
          <div class="den-settings-pref-copy"><span class="den-settings-pref-label">Release channel</span></div>
          <DenSelect value={state()?.channel ?? "stable"} disabled={locked()} data-testid="updates-channel" options={[{ value: "stable", label: "Stable" }, { value: "preview", label: "Preview" }]} aria-label="Release channel" onValueChange={(value) => { if (value === "stable" || value === "preview") void updates.run(() => updates.service.setChannel(value)); }} />
        </div>
        <Show when={state()?.install_source === "system_package"}><p class="den-settings-hint" data-testid="updates-package-channel">{nix() ? "This selects the upstream release feed. It does not change your Nix inputs, lock files, profiles or system generation." : "This selects the upstream release feed. It does not change your package repositories, holds or pins."}</p></Show>
      </div>
    </Show>
    <DenButton variant="secondary" {...(props.preferences ? settingAnchor("check-for-updates") : {})} data-testid="updates-check-now" disabled={updates.busy() || !state()?.capabilities.can_check} onClick={() => void updates.run(updates.service.check)}>{state()?.discovery === "checking" ? "Checking…" : "Check now"}</DenButton>
    <Show when={state()}>{(current) => <div class="den-settings-hint" data-testid="updates-status">
      <p data-testid="updates-current-version">Version {current().running_version}</p>
      <Show when={current().capabilities.blocked_reason === "development_build"}><p data-testid="updates-development-build">Updates are unavailable in development builds. Install a packaged release to use automatic updates.</p></Show>
      <Show when={current().discovery === "up_to_date"}><p>{current().install_source === "system_package" ? "The selected upstream release channel has no newer version. Package-source availability has not been checked." : "You’re using the latest version."}</p></Show>
      <Show when={current().discovery === "held_back"}><p data-testid="updates-rollout-eligibility">A newer release is rolling out gradually. Check now to get it sooner.</p></Show>
      <Show when={current().install_source !== "system_package" && candidate()}>{(release) => <p>Version {release().version} is available.</p>}</Show>
      <Show when={installationStatus(current())}>{(status) => <p role="status" aria-live="polite" aria-atomic="true" data-testid="updates-installation-status">{status()}{current().installation === "downloading" ? progress() : ""}</p>}</Show>
      <Show when={current().install_source === "system_package" && current().package}>{(pkg) => <>
