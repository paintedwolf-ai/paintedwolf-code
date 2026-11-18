import { createMemo, createSignal, Show, type Accessor } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { SessionMode } from "../../api/types.ts";
import { sessionModeView, setDraftSessionMode } from "../../chat/session/session-mode-state.ts";
import { upgradeSessionMode } from "../../chat/session/session-mode-upgrade.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { SessionModeSwitch } from "./SessionModeSwitch.tsx";

export function createChatViewMode(
  appStore: AppStore,
  sessionId: Accessor<string>,
  connectedClient: Accessor<LycaonClient | null>,
  reportError: () => (error: unknown) => void,
) {
  const [refocus, setRefocus] = createSignal(0);
  const view = createMemo(() => sessionModeView(appStore, sessionId()));
  const select = (mode: SessionMode) => {
    const id = sessionId();
    if (!view().started) {
      setDraftSessionMode(appStore, id, mode);
      return;
    }
    const client = connectedClient();
    if (client && mode === "agent") {
      void upgradeSessionMode(appStore, client, id).catch(reportError());
    }
  };

  return {
    view,
    refocus,
    slot: {
      present: () => view().show,
      onFocusExit: () => setRefocus((value) => value + 1),
      children: () => (
        <Show when={view().mode}>
          {(mode) => (
            <SessionModeSwitch
              mode={mode()}
              started={view().started}
              upgrade={view().upgrade}
              disabled={view().admissionPending || (!connectedClient() && view().started)}
              onSelect={select}
            />
          )}
        </Show>
      ),
    },
  };
}
