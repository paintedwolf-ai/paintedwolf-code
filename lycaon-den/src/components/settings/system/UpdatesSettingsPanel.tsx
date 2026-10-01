import { Show } from "solid-js";
import { isTauriRuntime } from "../../../platform/runtime.ts";
import { UpdatePanel, type UpdatePanelProps } from "../../update/UpdatePanel.tsx";

export function UpdatesSettingsPanel(props: Pick<UpdatePanelProps, "updateService">) {
  return (
    <Show
      when={props.updateService || isTauriRuntime()}
      fallback={
        <p class="den-settings-hint" data-testid="updates-unavailable">
          Update settings are available in the desktop app.
        </p>
      }
    >
      <UpdatePanel preferences updateService={props.updateService} />
    </Show>
  );
}
