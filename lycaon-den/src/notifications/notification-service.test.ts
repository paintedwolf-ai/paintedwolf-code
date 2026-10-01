import { afterEach, describe, expect, it, vi } from "vitest";
import type { SessionStatus } from "../api/types.ts";
import type { DenNotification } from "../platform/desktop/notifications.ts";
import type { DenNotificationPrefs } from "../../shared/app-state-types.ts";
import {
  MEANINGFUL_RUN_MS,
  NOTIFICATION_PRODUCT_TITLE,
  createNotificationService,
  type NotificationObserveState,
  type NotificationServiceDeps,
} from "./notification-service.ts";

function emptyState(
  overrides: Partial<{
    sessionStatus: Array<[string, SessionStatus]>;
    pendingUserInput: Array<[string, string]>;
    pendingCheckpoints: Array<[string, string]>;
  }> = {},
): NotificationObserveState {
  return {
    sessionStatus: new Map(overrides.sessionStatus ?? []),
    pendingUserInput: new Map(overrides.pendingUserInput ?? []),
    pendingCheckpoints: new Map(overrides.pendingCheckpoints ?? []),
  };
}

function makeDeps(
  overrides: Partial<NotificationServiceDeps> & {
    sends?: DenNotification[];
  } = {},
): NotificationServiceDeps & { sends: DenNotification[] } {
  const sends = overrides.sends ?? [];
  return {
    sends,
    isFocused: overrides.isFocused ?? (() => false),
    prefs: overrides.prefs ?? (() => ({})),
    runActiveMs: overrides.runActiveMs ?? (() => MEANINGFUL_RUN_MS + 1),
    sessionTitle: overrides.sessionTitle ?? (() => "My chat"),
    runOrdinal: overrides.runOrdinal ?? (() => 42),
    send:
      overrides.send ??
      (async (n) => {
        sends.push(n);
      }),
    ensurePermission: overrides.ensurePermission ?? (async () => "granted"),
    onPermissionResult: overrides.onPermissionResult ?? (() => undefined),
  };
}

describe("createNotificationService", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("Finished fires once on qualifying busy→idle", async () => {
    const deps = makeDeps();
    const svc = createNotificationService(deps);
    svc.observe(emptyState({ sessionStatus: [["s1", "busy"]] }));
    svc.observe(emptyState({ sessionStatus: [["s1", "idle"]] }));
    await vi.waitFor(() => expect(deps.sends).toHaveLength(1));
    expect(deps.sends[0]).toEqual({
      title: NOTIFICATION_PRODUCT_TITLE,
      body: "Finished — My chat",
      sessionId: "s1",
    });
    svc.observe(emptyState({ sessionStatus: [["s1", "idle"]] }));
    await Promise.resolve();
    expect(deps.sends).toHaveLength(1);
  });

  it("sub-threshold, missing run clock, and missing ordinal fire nothing", async () => {
    const deps = makeDeps({
      runActiveMs: (id) => {
        if (id === "short") return 100;
        if (id === "missing") return undefined;
        return MEANINGFUL_RUN_MS + 1;
      },
      runOrdinal: (id) => (id === "no-ord" ? undefined : 42),
    });
    const svc = createNotificationService(deps);
    svc.observe(
      emptyState({
        sessionStatus: [
          ["short", "busy"],
          ["missing", "busy"],
          ["no-ord", "busy"],
        ],
      }),
    );
    svc.observe(
      emptyState({
        sessionStatus: [
          ["short", "idle"],
          ["missing", "idle"],
          ["no-ord", "idle"],
        ],
      }),
    );
    await Promise.resolve();
    expect(deps.sends).toHaveLength(0);
  });

  it("focused window drops all classes", async () => {
    const deps = makeDeps({ isFocused: () => true });
    const svc = createNotificationService(deps);
    svc.observe(emptyState({ sessionStatus: [["s1", "busy"]] }));
    svc.observe(emptyState({ sessionStatus: [["s1", "idle"]] }));
    svc.observe(
      emptyState({
        sessionStatus: [["s1", "idle"]],
        pendingUserInput: [["s1", "ask-1"]],
        pendingCheckpoints: [["cp-1", "s1"]],
      }),
    );
    await Promise.resolve();
    expect(deps.sends).toHaveLength(0);
  });

  it("replaying the same batch does not re-fire (dedupe)", async () => {
    const deps = makeDeps();
    const svc = createNotificationService(deps);
    const idleWithPending = emptyState({
      sessionStatus: [["s1", "idle"]],
      pendingUserInput: [["s1", "ask-1"]],
      pendingCheckpoints: [["cp-1", "s1"]],
    });
    svc.observe(emptyState({ sessionStatus: [["s1", "busy"]] }));
    svc.observe(idleWithPending);
    await vi.waitFor(() => expect(deps.sends.length).toBeGreaterThanOrEqual(3));
    const count = deps.sends.length;
    svc.observe(idleWithPending);
    svc.observe(idleWithPending);
    await Promise.resolve();
    expect(deps.sends).toHaveLength(count);
  });

  it("main and per-class prefs gate correctly", async () => {
    let prefs: DenNotificationPrefs = { finished: false };
    const deps = makeDeps({ prefs: () => prefs });
    const svc = createNotificationService(deps);
    svc.observe(emptyState({ sessionStatus: [["s1", "busy"]] }));
    svc.observe(emptyState({ sessionStatus: [["s1", "idle"]] }));
    await Promise.resolve();
    expect(deps.sends).toHaveLength(0);

    prefs = { needsYou: false, needsApproval: true };
    svc.observe(
      emptyState({
        sessionStatus: [["s1", "idle"]],
        pendingUserInput: [["s1", "ask-2"]],
        pendingCheckpoints: [["cp-2", "s1"]],
      }),
    );
    await vi.waitFor(() => expect(deps.sends).toHaveLength(1));
    expect(deps.sends[0]?.body.startsWith("Needs your approval")).toBe(true);

    prefs = { enabled: false };
    svc.observe(
      emptyState({
        sessionStatus: [["s1", "idle"]],
        pendingCheckpoints: [
          ["cp-2", "s1"],
          ["cp-3", "s1"],
        ],
      }),
    );
    await Promise.resolve();
    expect(deps.sends).toHaveLength(1);
  });

  it.each(["denied", "unavailable", "granted"] as const)("publishes %s permission without changing its meaning", async (result) => {
    const observed = vi.fn();
    const deps = makeDeps({
      ensurePermission: async () => result,
      onPermissionResult: observed,
    });
    const svc = createNotificationService(deps);
    svc.observe(emptyState({ sessionStatus: [["s1", "busy"]] }));
    svc.observe(emptyState({ sessionStatus: [["s1", "idle"]] }));
    await vi.waitFor(() => expect(observed).toHaveBeenCalledWith(result));
    expect(deps.sends).toHaveLength(result === "granted" ? 1 : 0);
  });

  it("band raise on an open checkpoint emits no second NeedsApproval alert", async () => {
    const deps = makeDeps();
    const svc = createNotificationService(deps);
    // Seed empty, then a new pending checkpoint → one alert.
    svc.observe(emptyState({ sessionStatus: [["s1", "idle"]] }));
    svc.observe(
      emptyState({
        sessionStatus: [["s1", "idle"]],
        pendingCheckpoints: [["cp-band", "s1"]],
      }),
    );
    await vi.waitFor(() => expect(deps.sends).toHaveLength(1));
    expect(deps.sends[0]?.body.startsWith("Needs your approval")).toBe(true);
    // Same checkpoint id still pending (host raised consequence_band on re-publish).
    svc.observe(
      emptyState({
        sessionStatus: [["s1", "idle"]],
        pendingCheckpoints: [["cp-band", "s1"]],
      }),
    );
    await Promise.resolve();
    expect(deps.sends).toHaveLength(1);
  });
});
