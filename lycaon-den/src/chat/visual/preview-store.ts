import type { LycaonClient } from "../../api/client.ts";
import type {
  PreviewActionOverlay,
  PreviewAttachment,
  PreviewEvent,
} from "../../api/types.ts";

export type LivePreviewSnapshot = {
  /** Transcript session in which this projection renders. */
  sessionId: string;
  /** Session that actually holds the page and controls any recording. */
  holderSessionId: string;
  parentSessionId?: string;
  pageId: string;
  assistantMessageId: string;
  toolCallId: string;
  live: boolean;
  seq: number;
  /** Store arrival order, comparable across pages. */
  revision: number;
  /** Sequence of the newest JPEG frame; non-frame events do not advance it. */
  frameSeq?: number;
  jpegB64?: string;
  mime?: string;
  width?: number;
  height?: number;
  url?: string;
  title?: string;
  idle?: boolean;
  action?: PreviewActionOverlay | null;
};

const bySession = new Map<string, Map<string, LivePreviewSnapshot>>();
const pageInvocation = new Map<string, string>();
const pageSeq = new Map<string, number>();
const refreshTokens = new Map<string, symbol>();
const listeners = new Set<() => void>();
let revision = 0;

/** Map insertion order tracks session recency. */
const PREVIEW_SESSION_CAP = 64;

/** Completed snapshots retain their last frame; live pages are exempt from eviction. */
const PREVIEW_INVOCATIONS_PER_SESSION_CAP = 32;

function touchSession(sessionId: string): void {
  const map = bySession.get(sessionId);
  if (!map) return;
  bySession.delete(sessionId);
  bySession.set(sessionId, map);
}

function evictSession(sessionId: string): void {
  bySession.delete(sessionId);
  for (const [presentationId, map] of bySession) {
    for (const [key, snapshot] of map) {
      if (snapshot.holderSessionId === sessionId) map.delete(key);
    }
    if (map.size === 0) bySession.delete(presentationId);
  }
  const prefix = `${sessionId}\u0000`;
  for (const key of pageInvocation.keys()) {
    if (key.startsWith(prefix)) pageInvocation.delete(key);
  }
  for (const key of pageSeq.keys()) {
    if (key.startsWith(prefix)) pageSeq.delete(key);
  }
  for (const key of watchHolds.keys()) {
    if (key.startsWith(prefix)) watchHolds.delete(key);
  }
  refreshTokens.delete(sessionId);
}

function trimSessions(): void {
  while (bySession.size > PREVIEW_SESSION_CAP) {
    const oldest = bySession.keys().next().value;
    if (oldest === undefined) return;
    evictSession(oldest);
  }
}

function trimInvocations(map: Map<string, LivePreviewSnapshot>): void {
  if (map.size <= PREVIEW_INVOCATIONS_PER_SESSION_CAP) return;
  const finished = [...map.entries()]
    .filter(([, snap]) => !snap.live)
    .sort((left, right) => left[1].revision - right[1].revision);
  for (const [key] of finished) {
    if (map.size <= PREVIEW_INVOCATIONS_PER_SESSION_CAP) return;
    map.delete(key);
  }
}

function notify(): void {
  for (const listener of listeners) listener();
}

function invocationKey(assistantMessageId: string, toolCallId: string): string {
  return `${assistantMessageId.trim()}\u0000${toolCallId.trim()}`;
}

function physicalPageKey(holderSessionId: string, pageId: string): string {
  return `${holderSessionId.trim()}\u0000${pageId.trim()}`;
}

function sessionMap(sessionId: string): Map<string, LivePreviewSnapshot> {
  const sid = sessionId.trim();
  let map = bySession.get(sid);
  if (!map) {
    map = new Map();
    bySession.set(sid, map);
  }
  return map;
}

function presentationSessions(
  holderSessionId: string,
  parentSessionId?: string,
): string[] {
  const sessions = new Set<string>();
  if (holderSessionId.trim()) sessions.add(holderSessionId.trim());
  if (parentSessionId?.trim()) sessions.add(parentSessionId.trim());
  return [...sessions];
}

function snapshotForPhysicalPage(
  holderSessionId: string,
  pageId: string,
  activeKey: string,
): LivePreviewSnapshot | undefined {
  const snap = bySession.get(holderSessionId.trim())?.get(activeKey);
  return snap?.pageId === pageId.trim() ? snap : undefined;
}

function finishInvocation(
  holderSessionId: string,
  pageId: string,
  activeKey: string,
): LivePreviewSnapshot | undefined {
  let holderSnapshot: LivePreviewSnapshot | undefined;
  for (const map of bySession.values()) {
    const snap = map.get(activeKey);
    if (
      !snap ||
      snap.holderSessionId !== holderSessionId ||
      snap.pageId !== pageId
    ) {
      continue;
    }
    const ended = {
      ...snap,
      live: false,
      action: null,
      revision: ++revision,
    };
    map.set(activeKey, ended);
    if (ended.sessionId === holderSessionId) holderSnapshot = ended;
  }
  return holderSnapshot;
}

type PreviewState = PreviewEvent | PreviewAttachment;

function applyPreviewState(state: PreviewState, fromAttachment: boolean): void {
  const holderSessionId = state.session_id?.trim();
  const pageId = state.page_id?.trim();
  const assistantMessageId = state.assistant_message_id?.trim();
  const toolCallId = state.tool_call_id?.trim();
  if (!holderSessionId || !pageId || !assistantMessageId || !toolCallId) return;

  const physicalKey = physicalPageKey(holderSessionId, pageId);
  const seq = "seq" in state ? state.seq : 0;
  const lastSeq = pageSeq.get(physicalKey);
  if (!fromAttachment && lastSeq != null && seq <= lastSeq) return;
  if (fromAttachment && lastSeq != null) return;
  pageSeq.set(physicalKey, seq);

  const nextInvocationKey = invocationKey(assistantMessageId, toolCallId);
  const previousInvocationKey = pageInvocation.get(physicalKey);
  let previous = previousInvocationKey
    ? snapshotForPhysicalPage(holderSessionId, pageId, previousInvocationKey)
    : undefined;
  if (previousInvocationKey && previousInvocationKey !== nextInvocationKey) {
    previous =
      finishInvocation(holderSessionId, pageId, previousInvocationKey) ?? previous;
  }

  const op = "op" in state ? state.op : "attach";
  const parentSessionId = state.parent_session_id?.trim() || undefined;
  const existing = bySession.get(holderSessionId)?.get(nextInvocationKey);
  const inherited =
    previousInvocationKey !== nextInvocationKey ? previous : existing;
  const live = op !== "detach";
  const base: LivePreviewSnapshot = {
    sessionId: holderSessionId,
    holderSessionId,
    parentSessionId,
    pageId,
    assistantMessageId,
    toolCallId,
    live,
    seq,
    revision: ++revision,
    frameSeq:
      op === "frame" && "jpeg_b64" in state && state.jpeg_b64
        ? seq
        : inherited?.frameSeq,
    jpegB64:
      op === "frame" && "jpeg_b64" in state && state.jpeg_b64
        ? state.jpeg_b64
        : inherited?.jpegB64,
    mime: ("mime" in state ? state.mime : undefined) ?? inherited?.mime,
    width: state.width ?? inherited?.width,
    height: state.height ?? inherited?.height,
    url: state.url ?? inherited?.url,
    title: state.title ?? inherited?.title,
    idle: state.idle ?? inherited?.idle,
    action:
      op === "action" && "action" in state
        ? (state.action ?? null)
        : op === "detach"
          ? null
          : inherited?.action ?? null,
  };

  for (const sessionId of presentationSessions(holderSessionId, parentSessionId)) {
    const map = sessionMap(sessionId);
    map.set(nextInvocationKey, { ...base, sessionId });
    trimInvocations(map);
    touchSession(sessionId);
  }
  trimSessions();
  if (live) pageInvocation.set(physicalKey, nextInvocationKey);
  else pageInvocation.delete(physicalKey);
  notify();
}

/** Apply one invocation-scoped preview event. */
export function applyPreviewEvent(event: PreviewEvent): void {
  applyPreviewState(event, false);
}

/** Apply one held-page attachment. */
export function applyPreviewAttachment(attachment: PreviewAttachment): void {
  applyPreviewState(attachment, true);
}

export function getPreviewForInvocation(
  sessionId: string,
  assistantMessageId: string,
  toolCallId: string,
): LivePreviewSnapshot | undefined {
  return bySession
    .get(sessionId.trim())
    ?.get(invocationKey(assistantMessageId, toolCallId));
}

function newest(
  values: Iterable<LivePreviewSnapshot>,
  holderSessionId?: string,
): LivePreviewSnapshot | undefined {
  const holder = holderSessionId?.trim();
  let latest: LivePreviewSnapshot | undefined;
  for (const snap of values) {
    if (holder && snap.holderSessionId !== holder) continue;
    if (!latest || snap.revision > latest.revision) latest = snap;
  }
  return latest;
}

/** Every currently attached page projected into a session. */
export function getLivePreviewsForSession(
  sessionId: string,
): LivePreviewSnapshot[] {
  const map = bySession.get(sessionId.trim());
  if (!map) return [];
  return [...map.values()]
    .filter((snapshot) => snapshot.live)
    .sort((left, right) => left.revision - right.revision);
}

/** Parent task-card mirror for one worker-held page stream. */
export function getLatestPreviewForHolder(
  sessionId: string,
  holderSessionId: string,
): LivePreviewSnapshot | undefined {
  const map = bySession.get(sessionId.trim());
  return map ? newest(map.values(), holderSessionId) : undefined;
}

export function subscribeLivePreviewStore(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

type PreviewSnapshotClient = Pick<LycaonClient, "listSessionPreviews">;

/** Restore held-page identity from a host snapshot. */
export async function refreshLivePreviewSnapshots(
  client: PreviewSnapshotClient,
  sessionId: string,
): Promise<void> {
  const sid = sessionId.trim();
  if (!sid) return;
  const token = Symbol(sid);
  refreshTokens.set(sid, token);
  try {
    const attachments = await client.listSessionPreviews(sid);
    if (refreshTokens.get(sid) !== token) return;
    for (const attachment of attachments) applyPreviewAttachment(attachment);
  } finally {
    if (refreshTokens.get(sid) === token) refreshTokens.delete(sid);
  }
}

type PreviewWatchClient = Pick<LycaonClient, "watchPreview">;
const watchHolds = new Map<string, Set<symbol>>();

/** Refcount one visible page watch; concurrent held pages remain independent. */
export function acquirePreviewWatch(
  client: PreviewWatchClient,
  sessionId: string,
  pageId: string,
): () => void {
  const sid = sessionId.trim();
  const pid = pageId.trim();
  if (!sid || !pid) return () => {};
  const key = `${sid}\u0000${pid}`;
  let holds = watchHolds.get(key);
  if (!holds) {
    holds = new Set();
    watchHolds.set(key, holds);
  }
  const token = Symbol("preview-watch");
  holds.add(token);
  if (holds.size === 1) {
    void client
      .watchPreview(sid, { watching: true, page_id: pid })
      .catch(() => undefined);
  }
  return () => {
    const set = watchHolds.get(key);
    if (!set?.delete(token)) return;
    if (set.size === 0) {
      watchHolds.delete(key);
      void client
        .watchPreview(sid, { watching: false, page_id: pid })
        .catch(() => undefined);
    }
  };
}

/** Forget projections associated with a retired session. */
export function forgetLivePreviewSession(sessionId: string): void {
  const sid = sessionId.trim();
  if (!sid) return;
  evictSession(sid);
  notify();
}

export function resetLivePreviewStoreForTests(): void {
  bySession.clear();
  pageInvocation.clear();
  pageSeq.clear();
  listeners.clear();
  watchHolds.clear();
  refreshTokens.clear();
  revision = 0;
}

export function frameSrc(snap: LivePreviewSnapshot | undefined): string | null {
  if (!snap?.jpegB64) return null;
  const mime = snap.mime?.trim() || "image/jpeg";
  return `data:${mime};base64,${snap.jpegB64}`;
}
