import { Show, createSignal, onCleanup, onMount } from "solid-js";
import { nativeUpdateState } from "../../settings/system/update-state.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { UpdatePanel } from "./UpdatePanel.tsx";

export function UpdateStatusControl() {
  const [open, setOpen] = createSignal(false);
  let anchor: HTMLButtonElement | undefined;
  const state = nativeUpdateState.state;
  const visible = () => state()?.candidate || state()?.last_error || nativeUpdateState.error();
  onMount(() => { if (isTauriRuntime()) onCleanup(nativeUpdateState.mount()); });
  return <Show when={visible()}>
    <DenButton ref={(element) => { anchor = element; }} variant="ghost" aria-expanded={open()} aria-haspopup="dialog" onClick={() => setOpen(!open())}>
      {state()?.capabilities.can_restart_to_update ? "Update ready" : state()?.last_error || nativeUpdateState.error() ? "Update needs attention" : "Update available"}
    </DenButton>
    <Show when={open()}>
      <AnchoredSurface anchor={() => anchor} preferredSide="top" align="end" role="dialog" ariaLabel="Application update" minWidth={320} onDismiss={() => setOpen(false)}>
        <UpdatePanel />
      </AnchoredSurface>
    </Show>
  </Show>;
}
