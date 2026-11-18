import { createEffect, type Accessor } from "solid-js";
import { refreshFindings } from "../../chat/actions/findings-actions.ts";
import { refreshProgress } from "../../chat/progress/progress-actions.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { AppStore } from "../../store/app-state-model.ts";

type WorklogRefreshDeps = {
  appStore: AppStore;
  sessionId: Accessor<string>;
  offline: Accessor<boolean>;
  open: Accessor<boolean>;
};

/** Reads the session's progress and findings while its worklog is open. */
export function bindWorklogRefresh(deps: WorklogRefreshDeps): void {
  createEffect(() => {
    if (!deps.open()) return;
    const sessionId = deps.sessionId();
    if (!sessionId) return;
    if (deps.offline()) return;
    const client = getLycaonClient();
    if (!client) return;
    void refreshProgress(deps.appStore, client, sessionId).catch(
      () => undefined,
    );
    // refreshFindings records its own failures in the store.
    void refreshFindings(deps.appStore, client, sessionId);
  });
}
