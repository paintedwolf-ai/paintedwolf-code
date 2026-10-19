import { describe, expect, it, vi } from "vitest";
import type { AttentionRow } from "../api/types.ts";
import type { DenNotification, EnsureNotificationPermissionResult } from "../platform/desktop/notifications.ts";
import type { DenNotificationPrefs } from "../../shared/app-state-types.ts";
import { createNotificationService, MEANINGFUL_RUN_MS, NOTIFICATION_PRODUCT_TITLE, type NotificationServiceDeps } from "./notification-service.ts";

function row(id: string, reason: AttentionRow["reason"], since = "2026-10-18T00:00:00Z"): AttentionRow {
  return { session_id: id, project_id: `project-${id}`, title: `Chat ${id}`, reason, since_at: since,
    class: reason === "turn_finished" ? "finished" : reason === "turn_running" ? "running" : reason === "turn_error" ? "error" : "needs_you" };
}
function setup(overrides: Partial<NotificationServiceDeps> = {}) {
  const sends: DenNotification[] = [];
  const cancel = vi.fn(async (_: readonly string[]) => {});
  const deps: NotificationServiceDeps = {
    isFocused: () => false, prefs: () => ({}), connection: () => "engine-1",
    finishedRun: async r => ({ activeMs: MEANINGFUL_RUN_MS + 1, identity: `turn-${r.session_id}-${r.since_at}` }),
    send: async n => { sends.push(n); }, cancel,
    ensurePermission: async () => "granted", onPermissionResult: () => {}, ...overrides,
  };
  return { svc: createNotificationService(deps), sends, cancel, deps };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}

describe("device-wide chat notifications", () => {
  it("seeds only the first loaded snapshot and emits background completion and input across projects", async () => {
    const { svc, sends } = setup();
    svc.observe([], false);
    svc.observe([row("old", "turn_finished")]);
    svc.observe([row("old", "turn_finished"), row("background", "turn_finished"), row("other", "ask"), row("approval", "checkpoint")]);
    await vi.waitFor(() => expect(sends).toHaveLength(3));
    expect(sends).toContainEqual({ title: NOTIFICATION_PRODUCT_TITLE, body: "Finished — Chat background", sessionId: "background" });
    expect(sends.map(n => n.body)).toContain("Needs your input — Chat other");
    expect(sends.map(n => n.body)).toContain("Needs your approval — Chat approval");
  });

  it("does not notify for short, missing or unidentified runs", async () => {
    const read = vi.fn(async (r: AttentionRow) => r.session_id === "missing" ? undefined : ({ activeMs: r.session_id === "short" ? 100 : 5000, identity: r.session_id === "unknown" ? "" : "turn-1" }));
    const { svc, sends } = setup({ finishedRun: read });
    svc.observe([]);
    svc.observe([row("short", "turn_finished"), row("missing", "turn_finished"), row("unknown", "turn_finished")]);
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(3));
    expect(sends).toHaveLength(0);
  });

  it("deduplicates replay and metadata changes but permits a new ask identity", async () => {
    const { svc, sends } = setup();
    svc.observe([]);
    const first = row("s1", "ask");
    svc.observe([first]);
    await vi.waitFor(() => expect(sends).toHaveLength(1));
    svc.observe([{ ...first, title: "Renamed" }]);
    svc.observe([{ ...first, title: "Renamed" }]);
    await Promise.resolve();
    expect(sends).toHaveLength(1);
    svc.observe([row("s1", "ask", "2026-10-18T00:01:00Z")]);
    await vi.waitFor(() => expect(sends).toHaveLength(2));
  });

  it("retains the meaningful-turn identity when a finished row is republished", async () => {
    const { svc, sends } = setup({ finishedRun: async () => ({ activeMs: 5000, identity: "same-settled-turn" }) });
    svc.observe([]);
    svc.observe([row("s1", "turn_finished")]);
    await vi.waitFor(() => expect(sends).toHaveLength(1));
    svc.observe([]);
    svc.observe([row("s1", "turn_finished")]);
    await Promise.resolve();
    expect(sends).toHaveLength(1);
  });

  it("drops all classes while focused and honors enabled and per-class preferences", async () => {
    let prefs: DenNotificationPrefs = { enabled: false };
    let focused = false;
    const { svc, sends, deps } = setup({ prefs: () => prefs, isFocused: () => focused });
    svc.observe([]);
    svc.observe([row("disabled", "ask")]);
    await Promise.resolve();
    prefs = { needsYou: false, needsApproval: true, finished: false };
    svc.observe([row("ask", "ask"), row("finish", "turn_finished"), row("approval", "checkpoint")]);
    await vi.waitFor(() => expect(sends).toHaveLength(1));
    expect(sends[0]?.sessionId).toBe("approval");
    focused = true;
    svc.observe([row("focused", "checkpoint")]);
    await Promise.resolve();
    expect(sends).toHaveLength(1);
    expect(deps.isFocused()).toBe(true);
  });

  it.each(["granted", "denied", "unavailable"] as const)("records %s permission without claiming delivery", async permission => {
    const record = vi.fn();
    const { svc, sends } = setup({ ensurePermission: async () => permission, onPermissionResult: record });
    svc.observe([]); svc.observe([row("s1", "ask")]);
    await vi.waitFor(() => expect(record).toHaveBeenCalledWith(permission));
    expect(sends).toHaveLength(permission === "granted" ? 1 : 0);
  });

  it("withdraws settled or read sessions and aborts stale async clock work", async () => {
    const read = deferred<{ activeMs: number; identity: string } | undefined>();
    let signal!: AbortSignal;
    const started = vi.fn();
    const { svc, sends, cancel } = setup({ finishedRun: async (_, active) => { signal = active; started(); return read.promise; } });
    svc.observe([]); svc.observe([row("s1", "turn_finished")]);
    await vi.waitFor(() => expect(started).toHaveBeenCalled());
    svc.observe([]);
    expect(signal.aborted).toBe(true);
    read.resolve({ activeMs: 5000, identity: "old-turn" });
    await vi.waitFor(() => expect(cancel).toHaveBeenCalledWith(["s1"]));
    expect(sends).toHaveLength(0);
  });

  it("rechecks focus, preferences and presence after permission resolves", async () => {
    for (const changed of ["focus", "prefs", "read", "dispose", "connection"] as const) {
      const permission = deferred<EnsureNotificationPermissionResult>();
      let focused = false; let enabled = true; let connection = "engine-1";
      const started = vi.fn();
      const { svc, sends } = setup({ isFocused: () => focused, prefs: () => ({ enabled }), connection: () => connection, ensurePermission: async () => { started(); return permission.promise; } });
      svc.observe([]); svc.observe([row("s1", "ask")]);
      await vi.waitFor(() => expect(started).toHaveBeenCalled());
      if (changed === "focus") focused = true;
      if (changed === "prefs") enabled = false;
      if (changed === "read") svc.observe([]);
      if (changed === "dispose") svc.dispose();
      if (changed === "connection") connection = "engine-2";
      permission.resolve("granted");
      await Promise.resolve(); await Promise.resolve();
      expect(sends).toHaveLength(0);
    }
  });

  it("serializes withdrawal after an outstanding send and before its replacement", async () => {
    const delivered = deferred<void>();
    const order: string[] = [];
    const { svc } = setup({ send: async n => { order.push(n.body); if (order.length === 1) await delivered.promise; }, cancel: async () => { order.push("cancel"); } });
    svc.observe([]); svc.observe([row("s1", "ask")]);
    await vi.waitFor(() => expect(order).toHaveLength(1));
    svc.observe([row("s1", "checkpoint", "2026-10-18T00:01:00Z")]);
    expect(order).toHaveLength(1);
    delivered.resolve();
    await vi.waitFor(() => expect(order).toHaveLength(3));
    expect(order).toEqual(["Needs your input — Chat s1", "cancel", "Needs your approval — Chat s1"]);
  });

  it("resets on connection replacement and handles adapter failures without unhandled work", async () => {
    const { svc, sends } = setup({ ensurePermission: async () => { throw new Error("unavailable"); } });
    svc.observe([]); svc.observe([row("s1", "ask")]);
    svc.observe([], false);
    svc.observe([row("existing", "checkpoint")]);
    await Promise.resolve(); await Promise.resolve();
    expect(sends).toHaveLength(0);
  });
});
