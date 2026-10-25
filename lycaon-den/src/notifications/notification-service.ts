/** Device-wide host attention edges trigger native notifications. */
import type { AttentionRow } from "../api/types.ts";
import type { DenNotification, EnsureNotificationPermissionResult } from "../platform/desktop/notifications.ts";
import type { DenNotificationPrefs } from "../../shared/app-state-types.ts";
import { resolveNotificationPrefs } from "../settings/chat/notification-prefs.ts";

export const MEANINGFUL_RUN_MS = 4000;
export const NOTIFICATION_PRODUCT_TITLE = "Painted Wolf Code";
export const DEDUPE_CAP = 256;
export type NotificationClass = "Finished" | "NeedsYou" | "NeedsApproval";

const CLASS_BODY_LABEL: Record<NotificationClass, string> = {
  Finished: "Finished", NeedsYou: "Needs your input", NeedsApproval: "Needs your approval",
};

export interface NotificationServiceDeps {
  isFocused: () => boolean;
  prefs: () => DenNotificationPrefs;
  connection: () => unknown;
  /** Resolve the host clock for this session, independently of foreground caches. */
  finishedRun: (row: AttentionRow, signal: AbortSignal) => Promise<{ activeMs: number; identity: string } | undefined>;
  send: (notification: DenNotification) => Promise<unknown>;
  cancel: (sessionIds: readonly string[]) => Promise<void>;
  ensurePermission: () => Promise<EnsureNotificationPermissionResult>;
  onPermissionResult: (result: EnsureNotificationPermissionResult) => void;
}

function notificationClass(row: AttentionRow): NotificationClass | undefined {
  if (row.class === "finished" && row.reason === "turn_finished") return "Finished";
  if (row.class === "needs_you" && row.reason === "ask") return "NeedsYou";
  if (row.class === "needs_you" && row.reason === "checkpoint") return "NeedsApproval";
  return undefined;
}

function transitionKey(row: AttentionRow): string {
  return `${row.session_id}:${row.class}:${row.reason}:${row.since_at}`;
}

function enabled(prefs: DenNotificationPrefs, cls: NotificationClass): boolean {
  const resolved = resolveNotificationPrefs(prefs);
  return resolved.enabled && (cls === "Finished" ? resolved.finished : cls === "NeedsYou" ? resolved.needsYou : resolved.needsApproval);
}

export function createNotificationService(deps: NotificationServiceDeps) {
  let current = new Map<string, AttentionRow>();
  const pending = new Map<string, AbortController>();
  const chains = new Map<string, Promise<void>>();
  const seen = new Set<string>();
  let primed = false;
  let disposed = false;

  // A withdrawal follows an outstanding send before another notification for
  // that session can replace it. Async permission checks cannot reorder them.
  function enqueue(sessionId: string, action: () => Promise<void>): void {
    const work = (chains.get(sessionId) ?? Promise.resolve()).then(action).catch(() => undefined);
    chains.set(sessionId, work);
    void work.finally(() => { if (chains.get(sessionId) === work) chains.delete(sessionId); });
  }

  function stillCurrent(row: AttentionRow, signal: AbortSignal): boolean {
    const latest = current.get(row.session_id);
    return !disposed && !signal.aborted && !!latest && transitionKey(latest) === transitionKey(row);
  }

  async function emit(row: AttentionRow, cls: NotificationClass, controller: AbortController, connection: unknown): Promise<void> {
    const signal = controller.signal;
    const allowed = () => deps.connection() === connection && stillCurrent(row, signal) && !deps.isFocused() && enabled(deps.prefs(), cls);
    try {
      if (!allowed()) return;
      let discriminator = row.since_at;
      if (cls === "Finished") {
        const run = await deps.finishedRun(row, signal);
        if (!run || run.activeMs < MEANINGFUL_RUN_MS || !run.identity || !allowed()) return;
        discriminator = run.identity;
      }
      const key = `${cls}:${row.session_id}:${discriminator}`;
      if (seen.has(key)) return;
      seen.add(key);
      while (seen.size > DEDUPE_CAP) seen.delete(seen.values().next().value!);
      const permission = await deps.ensurePermission();
      if (!stillCurrent(row, signal) || deps.connection() !== connection) return;
      deps.onPermissionResult(permission);
      if (permission !== "granted" || !allowed()) return;
      const title = current.get(row.session_id)?.title?.trim() || "session";
      await deps.send({ title: NOTIFICATION_PRODUCT_TITLE, body: `${CLASS_BODY_LABEL[cls]} — ${title}`, sessionId: row.session_id });
    } finally {
      if (pending.get(row.session_id) === controller) pending.delete(row.session_id);
    }
  }

  function observe(rows: readonly AttentionRow[], loaded = true): void {
    if (disposed) return;
    const next = new Map(rows.filter(row => row.session_id).map(row => [row.session_id, { ...row }]));
    const previous = current;
    current = loaded ? next : new Map();
    for (const [sessionId, prior] of previous) {
      const row = current.get(sessionId);
      if (row && transitionKey(row) === transitionKey(prior)) continue;
      pending.get(sessionId)?.abort();
      pending.delete(sessionId);
      if (notificationClass(prior)) enqueue(sessionId, () => deps.cancel([sessionId]));
    }
    if (!loaded) { primed = false; return; }
    if (!primed) { primed = true; return; }
    for (const row of current.values()) {
      const prior = previous.get(row.session_id);
      const cls = notificationClass(row);
      if (!cls || prior && transitionKey(prior) === transitionKey(row)) continue;
      const controller = new AbortController();
      pending.set(row.session_id, controller);
      const connection = deps.connection();
      enqueue(row.session_id, () => emit(row, cls, controller, connection));
    }
  }

  return { observe, dispose: () => {
    disposed = true;
    for (const controller of pending.values()) controller.abort();
    pending.clear();
    current.clear();
  } };
}
