import { createStore } from "solid-js/store";
import type { LycaonClient } from "../api/client.ts";
import type { AttentionRow, AttentionView } from "../api/types.ts";
import { indexAttention, type AttentionIndex } from "../attention/attention-model.ts";

export type AttentionStoreState = {
  /** Non-idle sessions across every project, worst class first (host order). */
  rows: AttentionRow[];
  loaded: boolean;
};

export type AttentionSseBus = {
  onAttentionEvent: (cb: (view: AttentionView) => void) => () => void;
};

/** Device-wide events replace the complete attention view. */
export function createAttentionStore(
  getClient: () => LycaonClient | null,
  sse: AttentionSseBus,
) {
  const [state, setState] = createStore<AttentionStoreState>({
    rows: [],
    loaded: false,
  });

  let revision = 0;

  function apply(view: AttentionView | null | undefined): void {
    revision++;
    setState({ rows: [...(view?.rows ?? [])], loaded: true });
  }

  sse.onAttentionEvent(apply);

  // Reading rows keeps cached index consumers reactive.
  let cachedRows: readonly AttentionRow[] | null = null;
  let cachedIndex: AttentionIndex = indexAttention([]);

  return {
    state,
    /** Session-id lookup for row decoration. Reactive on state.rows. */
    index: (): AttentionIndex => {
      const rows = state.rows;
      if (rows !== cachedRows) {
        cachedRows = rows;
        cachedIndex = indexAttention(rows);
      }
      return cachedIndex;
    },
    /** Refresh after reconnect because attention events only fire on changes. */
    async load(): Promise<void> {
      const client = getClient();
      if (!client) return;
      const startedAt = ++revision;
      try {
        const view = await client.getAttention();
        if (revision === startedAt && client === getClient()) apply(view);
      } catch {
        // Preserve the last view when the background read fails.
      }
    },
    /** Apply a view directly — SSE path and tests. */
    apply,
    dropSession(sessionId: string): void {
      const id = sessionId.trim();
      if (!id) return;
      revision++;
      setState({ rows: state.rows.filter((row) => row.session_id !== id) });
    },
    dropProject(projectId: string): void {
      const id = projectId.trim();
      if (!id) return;
      revision++;
      setState({ rows: state.rows.filter((row) => row.project_id !== id) });
    },
    clear(): void {
      revision++;
      setState({ rows: [], loaded: false });
    },
  };
}

export type AttentionStore = ReturnType<typeof createAttentionStore>;
