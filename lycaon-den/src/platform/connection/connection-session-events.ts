import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { valueOf } from "../../store/load-state.ts";
import { refreshFindings } from "../../chat/actions/findings-actions.ts";
import { refreshProgress } from "../../chat/progress/progress-actions.ts";
import { refreshQueue } from "../../chat/actions/queue-actions.ts";
import type { TopicHandlers } from "../../api/events.ts";
export function connectionSessionEvents(appStore: AppStore, getClient: () => LycaonClient | null): TopicHandlers {
  return {
    findings: (ev, scope) => {
      const sessionId = scope.kind === "session" ? scope.session_id : undefined;
      if (sessionId !== appStore.state.currentSession?.id) return;
      const client = getClient();
      if (!sessionId || !client) return;
      const current = valueOf(appStore.state.findings)?.revision ?? 0;
      if (ev.revision <= current) return;
      void refreshFindings(appStore, client, sessionId, ev.revision);
    },
    progress: (ev, scope) => {
      const sessionId = scope.kind === "session" ? scope.session_id : undefined;
      if (sessionId !== appStore.state.currentSession?.id) return;
      const client = getClient();
      if (!sessionId || !client) return;
      const current = appStore.state.progress?.revision ?? 0;
      if (ev.revision <= current) return;
      void refreshProgress(appStore, client, sessionId, ev.revision).catch(
        () => undefined,
      );
    },
    queue: (ev, scope) => {
      const sessionId = scope.kind === "session" ? scope.session_id : undefined;
      if (sessionId !== appStore.state.currentSession?.id) return;
      const client = getClient();
      if (!sessionId || !client) return;
      const current = appStore.state.queueDraft?.revision ?? 0;
      if (ev.revision <= current) return;
      void refreshQueue(appStore, client, sessionId, ev.revision).catch(
        () => undefined,
      );
    },
  };
}
