import { usePresentationParticipant } from "../../../ui/presentation-context.tsx";
import { Show, createSignal, onMount } from "solid-js";
import type { JSX } from "solid-js";
import { DenButton } from "../../primitives/DenButton.tsx";
import {
  installShellCommand,
  shellCommandStatus,
  uninstallShellCommand,
  type ShellCommandStatus,
} from "../../../platform/desktop/shell-command.ts";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

/** Settings → Advanced: install or remove the `pw` shell command. */
export function ShellCommandPanel(): JSX.Element {
  const [status, setStatus] = createSignal<ShellCommandStatus | null>(null);
  const [error, setError] = createSignal<string | null>(null);
  const [busy, setBusy] = createSignal(false);

  usePresentationParticipant("shell-command-state", () => status() !== null || error() !== null);

  const refresh = async () => {
    try {
      setStatus(await shellCommandStatus());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  onMount(() => {
    void refresh();
  });

  const run = async (action: () => Promise<ShellCommandStatus>) => {
    setBusy(true);
    setError(null);
    try {
      setStatus(await action());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      await refresh();
    } finally {
      setBusy(false);
    }
  };

  const s = status;

  return (
    <div
      class="den-settings-subsection"
      data-testid="shell-command-panel"
      {...settingAnchor("command-line-tool")}
    >
      <h3 class="den-settings-subhead">{settingLabel("command-line-tool")}</h3>
      <p class="den-settings-hint">
        Install the <code>pw</code> command so you can open projects from a
        terminal with <code>pw .</code>. Symlinks go in{" "}
        <code>/usr/local/bin</code> (may ask for an administrator password).
        Crossbar also offers Install pw command.
      </p>

      <Show when={error()}>
        {(msg) => (
          <p class="den-settings-hint" data-testid="shell-command-error" role="alert">
            {msg()}
          </p>
        )}
      </Show>

      <Show when={s()?.unavailable_reason}>
        {(reason) => (
          <p class="den-settings-hint" data-testid="shell-command-unavailable">
            {reason()}
          </p>
        )}
      </Show>

      <Show when={s()} keyed>
        {(st) => (
          <>
            <p class="den-settings-hint" data-testid="shell-command-status">
              {st.installed
                ? `Installed — ${st.pw_link}`
                : "Not installed"}
            </p>

            <div class="den-settings-pref-row den-settings-actions">
              <Show
                when={st.installed}
                fallback={
                  <DenButton
                    variant="secondary"
                    disabled={busy() || !!st.unavailable_reason}
                    data-testid="shell-command-install"
                    onClick={() => void run(installShellCommand)}
                  >
                    Install pw command
                  </DenButton>
                }
              >
                <DenButton
                  variant="secondary"
                  disabled={busy()}
                  data-testid="shell-command-uninstall"
                  onClick={() => void run(uninstallShellCommand)}
                >
                  Uninstall
                </DenButton>
              </Show>
            </div>
          </>
        )}
      </Show>
    </div>
  );
}
