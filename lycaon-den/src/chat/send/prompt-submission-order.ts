import type { AppStore } from "../../store/app-state-model.ts";

type SubmissionLane = {
  tail: Promise<void>;
  pending: number;
  canceled: boolean;
};

const lanesByStore = new WeakMap<object, Map<string, SubmissionLane>>();

/** Synchronous reservation preserves send order through asynchronous preparation. */
export function reservePromptSubmission(appStore: AppStore, sessionId: string) {
  let lanes = lanesByStore.get(appStore.actions);
  if (!lanes) {
    lanes = new Map();
    lanesByStore.set(appStore.actions, lanes);
  }
  const lane = lanes.get(sessionId) ?? {
    tail: Promise.resolve(), pending: 0, canceled: false,
  };
  lanes.set(sessionId, lane);
  const currentLanes = lanes;
  const ready = lane.tail;
  let release!: () => void;
  lane.tail = new Promise<void>((resolve) => { release = resolve; });
  lane.pending += 1;
  let finished = false;
  return {
    ready,
    canceled: () => lane.canceled,
    finish: () => {
      if (finished) return;
      finished = true;
      lane.pending -= 1;
      if (lane.pending === 0 && currentLanes.get(sessionId) === lane) currentLanes.delete(sessionId);
      release();
    },
  };
}

export function cancelPromptSubmissions(appStore: AppStore, sessionId: string): void {
  const lanes = lanesByStore.get(appStore.actions);
  const lane = lanes?.get(sessionId);
  if (!lanes || !lane) return;
  lane.canceled = true;
  lanes.delete(sessionId);
}
