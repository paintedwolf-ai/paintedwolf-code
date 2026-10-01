import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { Show, createSignal, onCleanup, onMount } from "solid-js";
import type { JSX } from "solid-js";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { settingAnchor, settingLabel } from "../../settings/settings-registry.ts";
import {
  nativeUpdateService,
  type NativeUpdateState,
  type UpdateService,
} from "../../settings/system/update-service.ts";
import type { UpdateChannel } from "../../settings/system/update-service.ts";

import {
  updateError,
  UPDATE_ERROR_MESSAGES,
  type UpdateError,
} from "../../settings/system/update-error.ts";

const CHANNELS = [
  {
    value: "stable",
    label: "Stable",
    description: "Production releases only",
  },
  {
    value: "preview",
    label: "Preview",
    description: "Release candidates and production releases",
  },
] as const;

export type UpdatePanelProps = {
  preferences?: boolean;
  updateService?: UpdateService;
};

export function UpdatePanel(props: UpdatePanelProps): JSX.Element {
  const service = () => props.updateService ?? nativeUpdateService;
  const [checksOn, setChecksOn] = createSignal(true);
  const [state, setState] = createSignal<NativeUpdateState | null>(null);
  const [commandBusy, setCommandBusy] = createSignal(false);
  const [commandError, setCommandError] = createSignal<UpdateError | null>(null);

  let disposed = false;
  const acceptState = (next: NativeUpdateState) => {
    if (disposed || next.revision < (state()?.revision ?? 0)) return;
    setChecksOn(next.checks_enabled);
    setState({ ...next, error: next.error ? updateError(next.error) : undefined });
  };

  usePresentationParticipant("update-state", () => state() !== null || commandError() !== null);

  const run = async (operation: () => Promise<NativeUpdateState>) => {
    const startedRevision = state()?.revision ?? 0;
    setCommandBusy(true);
    setCommandError(null);
    try {
      acceptState(await operation());
    } catch (err) {
      setCommandError(updateError(err));
      // Native installation can finish before a command reply is lost.
      try {
        acceptState(await service().getState());
      } catch {
        // The latest native event remains usable if the read fails.
      }
      const current = state();
      if (current?.phase === "restart_required" && current.revision > startedRevision) {
        setCommandError(null);
      }
    } finally {
      setCommandBusy(false);
    }
  };

  onMount(() => {
    let unlisten = () => {};
    // Subscribe first to cover transitions during the initial read.
    void (async () => {
      let subscriptionError: UpdateError | null = null;
      try {
        const stop = await service().subscribe(acceptState);
        if (disposed) {
          stop();
          return;
        }
        unlisten = stop;
      } catch (err) {
        subscriptionError = updateError(err);
      }
      if (disposed) return;
      await run(() => service().getState());
      if (!disposed && subscriptionError) setCommandError(subscriptionError);
    })();
    onCleanup(() => {
      disposed = true;
      unlisten();
    });
  });

  const runInstall = () => {
    const current = state();
    if (
      (current?.phase !== "available" && current?.phase !== "restart_required") ||
      (current.phase !== "restart_required" && current.install_source !== "direct_download") ||
      !current.available_version
    ) {
      return;
    }
    const expectedVersion = current.available_version;
    void run(() => service().install(expectedVersion));
  };

  const busy = () =>
    commandBusy() || state()?.phase === "checking" || state()?.phase === "installing";

  return (
    <div class="den-settings-section" data-testid="updates-settings-panel">
      <Show when={props.preferences}>
      <div class="den-settings-pref-group">
        <div class="den-settings-pref-row" {...settingAnchor("update-checks")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("update-checks")}</span>
            <p class="den-settings-hint">
              Checks shortly after launch and every six hours by downloading one
              small file listing the latest version. It carries no identifier, no
              account, and nothing about how you use the app. New releases reach
              devices gradually over their first two days; Check now always offers
              the newest version. Turn this off to stop automatic checks. Check now and installation still contact the update server when you request them.
            </p>
          </div>
          <DenCheckbox
            checked={checksOn()}
            disabled={busy() || state() == null}
            data-testid="updates-check-enabled"
            onChange={(event) => {
              const enabled = event.currentTarget.checked;
              setChecksOn(enabled);
              setCommandError(null);
              void run(async () => {
                try {
                  return await service().setChecksEnabled(enabled);
                } catch (err) {
                  setChecksOn(state()?.checks_enabled ?? true);
                  throw err;
                }
              });
            }}
          >
            <span class="sr-only">{settingLabel("update-checks")}</span>
          </DenCheckbox>
        </div>
        <div class="den-settings-pref-row" {...settingAnchor("release-channel")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("release-channel")}</span>
            <p class="den-settings-hint">
              Preview receives release candidates as well as production releases.
              Switching channels never installs an older version.
            </p>
          </div>
          <DenSelect
            aria-label={settingLabel("release-channel")}
            data-testid="updates-channel"
            disabled={busy() || state() == null || state()?.phase === "restart_required"}
            options={CHANNELS}
            value={state()?.channel}
            onValueChange={(value) => {
              const channel = value as UpdateChannel;
              const current = state();
              if (!current || current.channel === channel) return;
              void run(() => service().setChannel(channel));
            }}
          />
        </div>
      </div>
      </Show>

      <DenButton
        variant="secondary"
        {...settingAnchor("check-for-updates")}
        data-testid="updates-check-now"
        disabled={busy() || (state() == null && commandError() == null) || state()?.phase === "restart_required"}
        onClick={() => { void run(() => service().check()); }}
      >
        {state()?.phase === "checking" || (commandBusy() && state() == null)
          ? "Checking…"
          : settingLabel("check-for-updates")}
      </DenButton>

      <Show when={state()} keyed>
        {(current) => (
          <div class="den-settings-hint" data-testid="updates-status">
            <p data-testid="updates-current-version">
              Version {current.current_version} · {current.channel} channel
            </p>
            <Show when={current.rollout_eligibility === "eligible"}>
              <p data-testid="updates-rollout-eligibility">
                This device is eligible for the current rollout.
              </p>
            </Show>
            <Show when={current.rollout_eligibility === "held_back"}>
              <p data-testid="updates-rollout-eligibility">
                This device is waiting for the current rollout.
              </p>
            </Show>
            <p>
              <Show when={current.phase === "up_to_date"}>
                You are on the latest version available to this channel.
              </Show>
              <Show when={current.phase === "held_back"}>
                Version {current.available_version} is being released gradually and is not
                available to this device yet.
              </Show>
            <Show when={current.phase === "unavailable"}>
              <Show when={!current.error}>Could not check for updates. Try again later.</Show>
            </Show>
            <Show when={current.phase === "restart_required"}>
              The update is installed. Restart the app to finish updating.
            </Show>
            <Show when={current.phase === "installing"}>
              Downloading, verifying, and installing version {current.available_version}…
              <Show when={current.total_bytes}>
                {(total) => (
                  <>
                    {Math.min(100, Math.round(current.downloaded_bytes / total() * 100))}% downloaded
                  </>
                )}
              </Show>
            </Show>
            <Show when={current.phase === "available"}>
              Version {current.available_version} is available. {" "}
              <Show
                when={current.install_source === "homebrew_cask"}
                fallback={
                  <Show when={current.install_source === "direct_download"}>
                    <span data-testid="updates-direct-install">The update is verified before it is applied.</span>
                  </Show>
                }
              >
                <span data-testid="updates-brew-upgrade">
                  You installed this with Homebrew — run <code>brew upgrade --cask
                  {current.channel === "preview"
                    ? "painted-wolf-code@preview"
                    : "painted-wolf-code"}</code> to update.
                </span>
              </Show>
            </Show>
            </p>
          </div>
        )}
      </Show>

      <Show when={state()?.phase === "available" && state()?.notes} keyed>
        {(notes) => (
          <p class="den-settings-hint" data-testid="updates-release-notes">
            {notes}
          </p>
        )}
      </Show>

      <Show
        when={
          state()?.phase === "restart_required" ||
          ((state()?.phase === "available" || state()?.phase === "installing") &&
            state()?.install_source === "direct_download")
        }
      >
        <DenButton
          variant="primary"
          data-testid="updates-install"
          disabled={busy()}
          onClick={runInstall}
        >
          {state()?.phase === "installing" ? "Installing…" : state()?.phase === "restart_required" ? "Restart now" : "Install and restart"}
        </DenButton>
      </Show>

      <Show when={commandError() ?? state()?.error} keyed>
        {(error) => (
          <div class="den-settings-hint" data-testid="updates-install-status">
            <p role="alert">{UPDATE_ERROR_MESSAGES[error.code]}</p>
            <Show when={error.detail} keyed>
              {(detail) => (
                <details>
                  <summary><span class="den-disclosure-caret" aria-hidden="true" />Technical details</summary>
                  <p>{detail}</p>
                </details>
              )}
            </Show>
          </div>
        )}
      </Show>
    </div>
  );
}
