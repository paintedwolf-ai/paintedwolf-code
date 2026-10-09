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
  const locked = () => updates.busy() || !state() || state()?.installation === "recovery_required";
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
            <p class="den-settings-hint">Downloads updates in the background and installs them when you quit or next open Painted Wolf Code. The app never restarts automatically while you’re using it.</p>
            <p class="den-settings-hint">Checks shortly after launch and every six hours. New releases reach devices gradually over their first two days; Check now always offers the newest version. Requests carry no device identifier or account information. Homebrew installations update through Homebrew.</p>
          </div>
          <DenCheckbox checked={state()?.automatic_updates_enabled ?? false} disabled={locked()} data-testid="updates-check-enabled" onChange={(event) => { const enabled = event.currentTarget.checked; void updates.run(() => updates.service.setAutomaticUpdatesEnabled(enabled)); }}><span class="sr-only">Automatic updates</span></DenCheckbox>
        </div>
        <div class="den-settings-pref-row" {...settingAnchor("release-channel")}>
          <div class="den-settings-pref-copy"><span class="den-settings-pref-label">Release channel</span></div>
          <DenSelect value={state()?.channel ?? "stable"} disabled={locked()} data-testid="updates-channel" options={[{ value: "stable", label: "Stable" }, { value: "preview", label: "Preview" }]} aria-label="Release channel" onValueChange={(value) => { if (value === "stable" || value === "preview") void updates.run(() => updates.service.setChannel(value)); }} />
        </div>
      </div>
    </Show>
    <DenButton variant="secondary" {...(props.preferences ? settingAnchor("check-for-updates") : {})} data-testid="updates-check-now" disabled={updates.busy() || !state()?.capabilities.can_check} onClick={() => void updates.run(updates.service.check)}>{state()?.discovery === "checking" ? "Checking…" : "Check now"}</DenButton>
    <Show when={state()}>{(current) => <div class="den-settings-hint" data-testid="updates-status">
      <p data-testid="updates-current-version">Version {current().running_version}</p>
      <Show when={current().discovery === "up_to_date"}><p>You’re using the latest version.</p></Show>
      <Show when={current().discovery === "held_back"}><p data-testid="updates-rollout-eligibility">A newer release is rolling out gradually. Check now to get it sooner.</p></Show>
      <Show when={candidate()}>{(release) => <p>Version {release().version} is available.</p>}</Show>
      <Show when={installationStatus(current())}>{(status) => <p role="status" aria-live="polite" aria-atomic="true" data-testid="updates-installation-status">{status()}{current().installation === "downloading" ? progress() : ""}</p>}</Show>
      <Show when={current().install_source === "homebrew_cask"}><p data-testid="updates-brew-upgrade">You installed this with Homebrew — run <code>brew upgrade --cask {current().channel === "preview" ? "painted-wolf-code@preview" : "painted-wolf-code"}</code> to update.</p></Show>
      <Show when={current().capabilities.blocked_reason === "unsupported_installation"}><p>This installation needs permission or a supported location to update.</p></Show>
      <Show when={current().capabilities.blocked_reason === "state_unavailable"}><p>Updates are paused because Painted Wolf Code could not coordinate with its installation. Close other copies and reopen the app.</p></Show>
    </div>}</Show>
    <Show when={candidate()?.notes} keyed>{(notes) => <div class="den-settings-hint update-release-notes" data-testid="updates-release-notes"><MarkdownBody untrusted source={notes} /></div>}</Show>
    <Show when={state()?.capabilities.can_download || state()?.capabilities.can_restart_to_update}>
      <DenButton variant="primary" data-testid="updates-install" disabled={updates.busy()} onClick={action}>{state()?.capabilities.can_restart_to_update ? "Restart to update" : state()?.installation === "failed" ? "Retry update" : "Download update"}</DenButton>
    </Show>
    <Show when={updates.error() ?? state()?.last_error}>{(error) => <div class="den-settings-hint" data-testid="updates-install-status"><p role="alert">{UPDATE_ERROR_MESSAGES[error().code]}</p><Show when={error().detail}>{(detail) => <details><summary><span class="den-disclosure-caret" aria-hidden="true" />Technical details</summary><p>{detail()}</p></details>}</Show></div>}</Show>
  </div>;
}
