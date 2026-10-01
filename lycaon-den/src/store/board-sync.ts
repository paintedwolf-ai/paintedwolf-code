import type { BoardEvent, BoardView } from "../api/types.ts";
import type { AppStoreActions } from "./app-state-model.ts";

/** Matches EventHub server debounce. */
export const BOARD_COALESCE_MS = 200;

export type BoardCoalescer = {
  schedule: (event: BoardEvent) => void;
  cancel: () => void;
  flush: () => void;
};

/** Debounce burst board SSE events into a single store update. */
export function createBoardCoalescer(
  apply: (snapshot: BoardView) => void,
): BoardCoalescer {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pending: BoardView | undefined;

  const flush = () => {
    timer = undefined;
    if (pending) {
      apply(pending);
      pending = undefined;
    }
  };

  return {
    schedule(event: BoardEvent) {
      pending = event.snapshot;
      if (timer) clearTimeout(timer);
      timer = setTimeout(flush, BOARD_COALESCE_MS);
    },
    cancel() {
      if (timer) clearTimeout(timer);
      timer = undefined;
      pending = undefined;
    },
    flush,
  };
}

export function createStoreBoardCoalescer(
  actions: AppStoreActions,
): BoardCoalescer {
  return createBoardCoalescer((snapshot) => actions.setBoard(snapshot));
}
