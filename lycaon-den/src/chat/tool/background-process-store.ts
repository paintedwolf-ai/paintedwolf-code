import type { LycaonClient } from "../../api/client.ts";
import type { BackgroundProcessEvent, BackgroundProcessOutput } from "../../api/types.ts";
import { ByteCache } from "../../utils/byte-cache.ts";

export type BackgroundProcessSnapshot = {
  processId: string;
  sessionId: string;
  running: boolean;
  exitCode?: number;
  truncated?: boolean;
  text: string;
};
type ProcessStatus = Omit<BackgroundProcessSnapshot, "text">;
type Interest = { sessionId?: string; processId?: string; statusOnly?: boolean };
const snapshots = new Map<string, ProcessStatus>();
const textCache = new ByteCache<string, string>(4 * 1024 * 1024, 512);
const hydratedSessions = new Set<string>();
const sessionRecency = new Map<string, true>();
const refreshTokens = new Map<string, { changed: boolean }>();
const listeners = new Map<() => void, (() => Interest) | undefined>();
const preparing = new Map<string, Promise<void>>();
const BACKGROUND_PROCESS_SESSION_CAP = 64;
const MAX_BACKGROUND_TEXT_CHARS = 8192;

function capText(text: string): string {
  if (text.length <= MAX_BACKGROUND_TEXT_CHARS) return text;
  let first = text.length - MAX_BACKGROUND_TEXT_CHARS;
  const code = text.charCodeAt(first);
  if (code >= 0xdc00 && code <= 0xdfff) first++;
  return text.slice(first);
}
function rememberText(id: string, text: string): void {
  const retained = capText(text);
  textCache.set(id, retained, 2 * (id.length + retained.length) + 64);
}
function touchSession(sessionId: string): void {
  sessionRecency.delete(sessionId); sessionRecency.set(sessionId, true);
  while (sessionRecency.size > BACKGROUND_PROCESS_SESSION_CAP) {
    const oldest = sessionRecency.keys().next().value;
    if (oldest === undefined) break;
    evictSession(oldest);
  }
}
function evictSession(sessionId: string): void {
  for (const id of preparing.keys()) if (id.startsWith(`${sessionId}:`)) preparing.delete(id);
  for (const [id, snap] of snapshots) if (snap.sessionId === sessionId) { snapshots.delete(id); textCache.delete(id); }
  hydratedSessions.delete(sessionId); sessionRecency.delete(sessionId); refreshTokens.delete(sessionId);
}
function key(sessionId: string, processId: string): string { return `${sessionId}:${processId}`; }
function notify(sessionId: string, processId?: string, statusChanged = true): void {
  for (const [listener, interest] of listeners) {
    const scope = interest?.();
    if (scope?.sessionId && scope.sessionId !== sessionId) continue;
    if (processId && scope?.processId && scope.processId !== processId) continue;
    if (scope?.statusOnly && !statusChanged) continue;
    listener();
  }
}
export function applyBackgroundProcessEvent(ev: BackgroundProcessEvent): void {
  const sessionId = ev.session_id?.trim(); const processId = ev.process_id?.trim();
  if (!sessionId || !processId) return;
  const refresh = refreshTokens.get(sessionId); if (refresh) refresh.changed = true;
  const id = key(sessionId, processId); const prev = snapshots.get(id);
  const before = textCache.get(id) ?? "";
  const text = ev.stream === "exit" ? before : ev.reset ? ev.text ?? "" : before + (ev.text ?? "");
  rememberText(id, text);
  snapshots.set(id, { processId, sessionId, running: ev.running, exitCode: ev.exit_code ?? prev?.exitCode,
    truncated: text.length > MAX_BACKGROUND_TEXT_CHARS || ev.truncated || prev?.truncated });
  touchSession(sessionId);
  notify(sessionId, processId, !prev || prev.running !== ev.running || (ev.exit_code !== undefined && prev.exitCode !== ev.exit_code));
}
type BackgroundProcessSnapshotClient = Pick<LycaonClient, "listSessionBackgroundProcesses" | "getSessionBackgroundProcessOutput">;

/** Status and prepared output arrive together, without one fetch per process. */
export async function refreshBackgroundProcessSnapshots(client: BackgroundProcessSnapshotClient, sessionId: string): Promise<void> {
  const sid = sessionId.trim(); if (!sid) return;
  const token = { changed: false }; refreshTokens.set(sid, token);
  try {
    while (refreshTokens.get(sid) === token) {
      token.changed = false;
      const processes = await client.listSessionBackgroundProcesses(sid);
      if (refreshTokens.get(sid) !== token) return;
      if (token.changed) continue;
      applyBackgroundProcessOutputs(sid, processes.map(process => process.output ?? {
        process_id: process.process_id, running: process.running, exit_code: process.exit_code, chunks: [],
      }));
      return;
    }
  } finally { if (refreshTokens.get(sid) === token) refreshTokens.delete(sid); }
}

function applyOutput(sessionId: string, output: BackgroundProcessOutput): void {
  const id = key(sessionId, output.process_id);
  const text = output.chunks.map(chunk => chunk.text).join("");
  snapshots.set(id, { processId: output.process_id, sessionId, running: output.running, exitCode: output.exit_code,
    truncated: output.truncated || text.length > MAX_BACKGROUND_TEXT_CHARS });
  rememberText(id, text);
}
/** Replace a session's process mirror from its authoritative snapshot. */
export function applyBackgroundProcessOutputs(sessionId: string, outputs: readonly BackgroundProcessOutput[]): void {
  const sid = sessionId.trim(); if (!sid) return;
  refreshTokens.delete(sid);
  const visible = new Set(outputs.map(output => key(sid, output.process_id)));
  for (const [id, snapshot] of snapshots) if (snapshot.sessionId === sid && !visible.has(id)) { snapshots.delete(id); textCache.delete(id); }
  for (const output of outputs) applyOutput(sid, output);
  hydratedSessions.add(sid); touchSession(sid); notify(sid);
}
/** Prepare a nearby card's bounded output; reject a response overtaken by events. */
export function prepareBackgroundProcessOutput(client: BackgroundProcessSnapshotClient, sessionId: string, processId: string): Promise<void> {
  const id = key(sessionId, processId); const before = snapshots.get(id);
  if (before && textCache.get(id) !== undefined) return Promise.resolve();
  const pending = preparing.get(id); if (pending) return pending;
  const request = client.getSessionBackgroundProcessOutput(sessionId, processId).then(output => {
    if (preparing.get(id) !== request || snapshots.get(id) !== before) return;
    applyOutput(sessionId, output); touchSession(sessionId); notify(sessionId, processId);
  }).finally(() => { if (preparing.get(id) === request) preparing.delete(id); });
  preparing.set(id, request);
  return request;
}
export function getBackgroundProcessSnapshot(sessionId: string, processId: string): BackgroundProcessSnapshot | undefined {
  const id = key(sessionId, processId); const status = snapshots.get(id);
  return status ? { ...status, text: textCache.get(id) ?? "" } : undefined;
}
export function hasHydratedBackgroundProcesses(sessionId: string): boolean { return hydratedSessions.has(sessionId.trim()); }
export function subscribeBackgroundProcessStore(listener: () => void, interest?: () => Interest): () => void {
  listeners.set(listener, interest); return () => { listeners.delete(listener); };
}
export function forgetBackgroundProcessSession(sessionId: string): void {
  const sid = sessionId.trim(); if (!sid) return;
  evictSession(sid); notify(sid);
}
export function resetBackgroundProcessStoreForTests(): void {
  snapshots.clear(); textCache.clear(); hydratedSessions.clear(); sessionRecency.clear(); refreshTokens.clear(); preparing.clear(); listeners.clear();
}
