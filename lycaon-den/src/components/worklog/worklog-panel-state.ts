import { createEffect, createSignal, type Accessor } from "solid-js";
import { refreshFindings } from "../../chat/actions/findings-actions.ts";
import { refreshProgress } from "../../chat/progress/progress-actions.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { AppStore } from "../../store/app-state-model.ts";

export type WorklogPanelStateDeps = {
  appStore: AppStore;
  sessionId: Accessor<string>;
  offline: Accessor<boolean>;
};

export type WorklogPanelState = {
  isOpen: Accessor<boolean>;
  open: () => void;
  close: () => void;
};

export function createWorklogPanelState(
  deps: WorklogPanelStateDeps,
): WorklogPanelState {
  const [isOpen, setIsOpen] = createSignal(false);

  createEffect(() => {
    if (!isOpen()) return;
    const sessionId = deps.sessionId();
    if (!sessionId) return;
    const offline = deps.offline();
    if (offline) return;
    const client = getLycaonClient();
    if (!client) return;
    void refreshProgress(deps.appStore, client, sessionId).catch(
      () => undefined,
    );
    // refreshFindings records its own failures in the store.
    void refreshFindings(deps.appStore, client, sessionId);
  });

  return {
    isOpen,
    open: () => setIsOpen(true),
    close: () => setIsOpen(false),
  };
}
