import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { Show, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

type Props = {
  client?: LycaonClient | null;
};

export function PowerSettingsPanel(props: Props) {
  const query = createSurfaceQuery({
    name: "power-settings",
    source: () => props.client ? { client: props.client, key: "device" } : null,
    load: ({ client }) => client.getPowerSettings(),
  });
  const status = query.value;
  const [saving, setSaving] = createSignal(false);
  const [actionError, setError] = createSignal<string | null>(null);
  const error = () => actionError() ?? query.error();
  const supported = () => status()?.supported ?? false;

  const save = (enabled: boolean) => {
    const client = props.client;
    const previous = status();
    if (!client || !previous) return;
    const target = query.capture();
    target.publish({ ...previous, keep_awake_while_working: enabled });
    setSaving(true);
    setError(null);
    void client.updatePowerSettings({ keep_awake_while_working: enabled })
      .then(target.publish)
      .catch((cause) => {
        target.publish(previous);
        setError(cause instanceof Error ? cause.message : String(cause));
      })
      .finally(() => setSaving(false));
  };

  return (
    <div class="den-settings-section" data-testid="power-settings-panel">
      <div class="den-settings-pref-group">
        <div class="den-settings-pref-row" {...settingAnchor("keep-awake")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("keep-awake")}</span>
            <p class="den-settings-hint">
              Prevents idle sleep while a session, worker, or security scan is active.
              The display can still turn off, and closing the lid can still put the Mac
              to sleep.
            </p>
            <Show when={status() != null && !supported()}>
              <p class="den-settings-hint">This setting is available on macOS.</p>
            </Show>
            <Show when={status()?.inhibiting}>
              <p class="den-settings-hint" data-testid="power-active-status">
                Keeping this Mac awake for active work.
              </p>
            </Show>
            <Show when={status()?.last_error}>
              {(message) => <p class="den-settings-warn" role="alert">{message()}</p>}
            </Show>
            <Show when={error()}>
              {(message) => <p class="den-settings-warn" role="alert">{message()}</p>}
            </Show>
          </div>
          <DenCheckbox
            checked={status()?.keep_awake_while_working ?? true}
            disabled={!props.client || !supported() || saving()}
            data-testid="power-keep-awake"
            onChange={(event) => save(event.currentTarget.checked)}
          >
            <span class="sr-only">{settingLabel("keep-awake")}</span>
          </DenCheckbox>
        </div>
      </div>
    </div>
  );
}
