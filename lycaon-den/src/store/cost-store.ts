import { untrack } from "solid-js";
import { createStore } from "solid-js/store";
import type { LycaonClient } from "../api/client.ts";
import type { CostSummary } from "../api/types.ts";

export type CostStoreState = {
  session?: CostSummary;
  sessionId?: string;
  loading: boolean;
  liveConsumers: number;
};

export type CostStore = {
  state: CostStoreState;
  actions: {
    clear: () => void;
  };
  refreshSession: (
    client: LycaonClient,
    sessionId: string,
  ) => Promise<CostSummary | undefined>;
  holdLiveRefresh: () => () => void;
  /** Registers an external refresh listener. */
  onInvalidated: (listener: () => void) => () => void;
  /** Notifies external refresh listeners. */
  emitInvalidated: () => void;
};

export function createCostStore(): CostStore {
  const [state, setState] = createStore<CostStoreState>({
    loading: false,
    liveConsumers: 0,
  });
  const listeners = new Set<() => void>();
  let requestVersion = 0;

  return {
    state,
    actions: {
      clear() {
        requestVersion += 1;
        // Live consumers have independent lifetimes.
        setState({
          session: undefined,
          sessionId: undefined,
          loading: false,
        });
      },
    },
    async refreshSession(client, sessionId) {
      const normalizedSessionId = sessionId.trim();
      if (!normalizedSessionId) return undefined;
      const version = ++requestVersion;
      // Callers refresh from effects; tracking this read would re-run them on the write below.
      if (untrack(() => state.sessionId) !== normalizedSessionId) {
        setState({
          session: undefined,
          sessionId: normalizedSessionId,
          loading: true,
        });
      } else {
        setState("loading", true);
      }
      try {
        const summary = await client.getCostSummary(normalizedSessionId);
        if (version !== requestVersion || state.sessionId !== normalizedSessionId) {
          return undefined;
        }
        setState({ session: summary, sessionId: normalizedSessionId, loading: false });
        return summary;
      } catch {
        return undefined;
      } finally {
        if (version === requestVersion && state.sessionId === normalizedSessionId) {
          setState("loading", false);
        }
      }
    },
    holdLiveRefresh() {
      setState("liveConsumers", (n) => n + 1);
      let released = false;
      return () => {
        if (released) return;
        released = true;
        setState("liveConsumers", (n) => Math.max(0, n - 1));
      };
    },
    onInvalidated(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    emitInvalidated() {
      for (const listener of [...listeners]) listener();
    },
  };
}
