/** Host contributions cached for startup. */
import { createSignal, untrack } from "solid-js";
import { clearContributionFrameCache, hasContributionFrameShape, readContributionFrameCache, writeContributionFrameCache } from "./contribution-frame-cache.ts";
import type { LycaonClient } from "../api/client.ts";
import type {
  ContributionFrameResponse,
  ContributionTheme,
} from "../api/types.ts";
import { clientNoticeError } from "../notices/client-notices.ts";
import type { NoticeReporter } from "../notices/notice-store.ts";

type ClientThunk = () => LycaonClient | null;

const RETRY_DELAYS_MS = [300, 900, 2_700, 8_000] as const;

export type ContributionFrameState = Readonly<
  | { phase: "ready"; frame: ContributionFrameResponse }
  | { phase: "unavailable"; frame: null }
>;

const bootFrame = readContributionFrameCache();
const INITIAL_FRAME_STATE: ContributionFrameState = bootFrame
  ? { phase: "ready", frame: bootFrame }
  : { phase: "unavailable", frame: null };

const [frameStateSignal, commitFrameState] =
  createSignal<ContributionFrameState>(INITIAL_FRAME_STATE);

const [readyIdentitySignal, setReadyIdentitySignal] = createSignal(
  bootFrame ? `ready:${bootFrame.frame_revision}` : "unready",
);

function setFrameStateSignal(state: ContributionFrameState): void {
  commitFrameState(state);
  const next =
    state.phase === "ready" ? `ready:${state.frame.frame_revision}` : "unready";
  if (next !== untrack(readyIdentitySignal)) setReadyIdentitySignal(next);
}

let clientThunk: ClientThunk | null = null;
let reporterThunk: (() => NoticeReporter | null) | null = null;
let inflight: Promise<void> | null = null;
let rerunQueued = false;
let sleep: (ms: number) => Promise<void> = (ms) =>
  new Promise((resolve) => setTimeout(resolve, ms));

export function contributionFrame(): ContributionFrameResponse | null {
  return frameStateSignal().frame;
}

export function contributionFrameState(): ContributionFrameState {
  return frameStateSignal();
}

export function presentedContributionThemes():
  | readonly ContributionTheme[]
  | null {
  return frameStateSignal().frame?.themes ?? null;
}

export function contributionFrameReady(): boolean {
  return readyIdentitySignal().startsWith("ready:");
}

export function initContributionStore(
  thunk: ClientThunk,
  notices?: () => NoticeReporter | null,
): void {
  clientThunk = thunk;
  reporterThunk = notices ?? null;
  if (clientThunk()) void ensureContributionFrame();
}

export function ensureContributionFrame(): Promise<void> {
  if (inflight) return inflight;
  if (frameStateSignal().phase === "ready" && !clientThunk?.()) {
    return Promise.resolve();
  }
  return startHydrate();
}

/** Queues one fresh frame after an active hydrate. */
export function invalidateContributionFrame(): Promise<void> {
  if (inflight) {
    rerunQueued = true;
    return inflight;
  }
  return startHydrate();
}

function startHydrate(): Promise<void> {
  inflight = hydrateOnce().finally(() => {
    inflight = null;
    if (rerunQueued) {
      rerunQueued = false;
      void startHydrate();
    }
  });
  return inflight;
}

async function hydrateOnce(): Promise<void> {
  for (let attempt = 0; ; attempt++) {
    const client = clientThunk?.();
    if (client) {
      try {
        const frame = await client.getContributions();
        if (!hasContributionFrameShape(frame)) throw new Error("The contribution frame has an incompatible shape.");
        const ready: ContributionFrameState = { phase: "ready", frame };
        setFrameStateSignal(ready);
        writeContributionFrameCache(frame);
        notifyChanged(ready);
        return;
      } catch {
        /* Retry, then report unavailable. */
      }
    }
    if (attempt >= RETRY_DELAYS_MS.length) break;
    await sleep(RETRY_DELAYS_MS[attempt]!);
    if (rerunQueued) return;
  }
  if (frameStateSignal().phase === "ready") return;
  const unavailable: ContributionFrameState = {
    phase: "unavailable",
    frame: null,
  };
  setFrameStateSignal(unavailable);
  notifyChanged(unavailable);
  reporterThunk?.()?.reportError(clientNoticeError("contribution_frame_unavailable"));
}

const changeListeners = new Set<(state: ContributionFrameState) => void>();

export function onContributionFrameChange(
  listener: (state: ContributionFrameState) => void,
): () => void {
  changeListeners.add(listener);
  return () => {
    changeListeners.delete(listener);
  };
}

function notifyChanged(state: ContributionFrameState): void {
  for (const listener of changeListeners) listener(state);
}

export function resetContributionStoreForTest(): void {
  clientThunk = null;
  reporterThunk = null;
  inflight = null;
  rerunQueued = false;
  clearContributionFrameCache();
  setFrameStateSignal({ phase: "unavailable", frame: null });
  changeListeners.clear();
}

export function setContributionRetrySleepForTest(
  fn: (ms: number) => Promise<void>,
): void {
  sleep = fn;
}

export function seedContributionFrameForTest(
  frame: ContributionFrameResponse,
): void {
  const ready: ContributionFrameState = { phase: "ready", frame };
  setFrameStateSignal(ready);
  notifyChanged(ready);
}

/** Loads the cached frame without a host request. */
export function replayContributionFrameMemoForTest(): void {
  const frame = readContributionFrameCache();
  setFrameStateSignal(
    frame ? { phase: "ready", frame } : { phase: "unavailable", frame: null },
  );
}
