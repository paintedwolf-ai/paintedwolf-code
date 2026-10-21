import { beforeEach, describe, expect, it, vi } from "vitest";
import { createStore } from "solid-js/store";
import type { LycaonClient } from "../api/client.ts";
import type { AttentionRow } from "../api/types.ts";
import type { AttentionStore } from "../store/attention-store.ts";
import type { NotificationServiceDeps } from "./notification-service.ts";

const mocks = vi.hoisted(() => ({
  tauri: { value: true },
  createService: vi.fn(),
  observe: vi.fn(),
  dispose: vi.fn(),
  badge: vi.fn(async () => undefined),
  focus: vi.fn(async () => undefined),
  open: vi.fn(),
  unsubscribe: vi.fn(),
  activated: [] as Array<(sessionId: string) => void>,
  finishedRun: vi.fn(() => ({ activeMs: 1, identity: "turn-1" })),
}));

vi.mock("../platform/runtime.ts", () => ({ isTauriRuntime: () => mocks.tauri.value }));
vi.mock("../platform/desktop/notifications.ts", () => ({
  cancelNotificationsForSessions: vi.fn(),
  ensureNotificationPermission: vi.fn(),
  sendNotification: vi.fn(),
  onNotificationActivated: (cb: (sessionId: string) => void) => {
    mocks.activated.push(cb);
    return mocks.unsubscribe;
  },
}));
vi.mock("../platform/windows/window-chrome.ts", () => ({
  focusAppWindow: mocks.focus,
  windowFocused: () => false,
}));
vi.mock("../platform/desktop/badge.ts", () => ({ setWaitingBadge: mocks.badge }));
vi.mock("../settings/chat/notification-prefs.ts", () => ({
  notificationPrefsLive: () => ({}),
  recordNotificationPermission: vi.fn(),
}));
vi.mock("./notification-open.ts", () => ({ openSessionFromNotification: mocks.open }));
vi.mock("./finished-run.ts", () => ({ finishedRun: mocks.finishedRun }));
vi.mock("./notification-service.ts", () => ({
  createNotificationService: (deps: NotificationServiceDeps) => {
    mocks.createService(deps);
    return { observe: mocks.observe, dispose: mocks.dispose };
  },
}));

import { mountNotificationService } from "./mount-notification-service.ts";

function row(sessionId: string, cls: AttentionRow["class"]): AttentionRow {
  return { session_id: sessionId, class: cls } as AttentionRow;
}

function attention(rows: AttentionRow[]) {
  const [state, setState] = createStore({ rows, loaded: true });
  return { store: { state } as unknown as AttentionStore, setState };
}

function deps(): NotificationServiceDeps {
  return mocks.createService.mock.calls.at(-1)![0] as NotificationServiceDeps;
}

describe("mountNotificationService", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.activated.length = 0;
    mocks.tauri.value = true;
  });

  it("does nothing outside the desktop shell", () => {
    mocks.tauri.value = false;
    const dispose = mountNotificationService({ attention: attention([]).store, getClient: () => null });
    dispose();
    expect(mocks.createService).not.toHaveBeenCalled();
  });

  it("observes attention rows and badges the sessions that need the person", () => {
    const { store, setState } = attention([row("a", "needs_you"), row("b", "finished")]);
    const dispose = mountNotificationService({ attention: store, getClient: () => null });
    expect(mocks.observe).toHaveBeenLastCalledWith(store.state.rows, true);
    expect(mocks.badge).toHaveBeenLastCalledWith(1);
    setState("rows", [row("a", "needs_you"), row("c", "needs_you")]);
    expect(mocks.badge).toHaveBeenLastCalledWith(2);
    expect(mocks.observe).toHaveBeenCalledTimes(2);
    dispose();
  });

  it("reads a finished run from the connection that is still current", async () => {
    const snapshot = { session: {} };
    const getSessionBootstrap = vi.fn(async () => snapshot);
    let client: LycaonClient | null = { getSessionBootstrap } as unknown as LycaonClient;
    const dispose = mountNotificationService({ attention: attention([]).store, getClient: () => client });
    const finished = row("a", "finished");

    await expect(deps().finishedRun(finished, new AbortController().signal))
      .resolves.toEqual({ activeMs: 1, identity: "turn-1" });
    expect(getSessionBootstrap).toHaveBeenCalledWith("a", expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(mocks.finishedRun).toHaveBeenCalledWith(snapshot, finished);

    const aborted = new AbortController();
    getSessionBootstrap.mockImplementationOnce(async () => { aborted.abort(); return snapshot; });
    await expect(deps().finishedRun(finished, aborted.signal)).resolves.toBeUndefined();

    const replaced = { getSessionBootstrap: vi.fn() } as unknown as LycaonClient;
    getSessionBootstrap.mockImplementationOnce(async () => { client = replaced; return snapshot; });
    await expect(deps().finishedRun(finished, new AbortController().signal)).resolves.toBeUndefined();

    client = null;
    await expect(deps().finishedRun(finished, new AbortController().signal)).resolves.toBeUndefined();
    expect(mocks.finishedRun).toHaveBeenCalledTimes(1);
    dispose();
  });

  it("opens the chat a notification names and releases everything on dispose", () => {
    const dispose = mountNotificationService({ attention: attention([]).store, getClient: () => null });
    mocks.activated[0]!("chat-a");
    expect(mocks.focus).toHaveBeenCalled();
    expect(mocks.open).toHaveBeenCalledWith("chat-a");
    dispose();
    expect(mocks.unsubscribe).toHaveBeenCalled();
    expect(mocks.dispose).toHaveBeenCalled();
  });
});
