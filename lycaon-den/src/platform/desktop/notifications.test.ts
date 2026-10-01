import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const isPermissionGranted = vi.fn(async (): Promise<boolean> => false);
const requestPermission = vi.fn(
  async (): Promise<"granted" | "denied" | "default"> => "denied",
);
const sendNotification = vi.fn(() => undefined);
const invoke = vi.fn();
const listen = vi.fn(
  async (
    _event: string,
    cb: (event: { payload: { sessionId?: string | null } }) => void,
  ) => {
    listenHandler = cb;
    return () => {
      listenHandler = undefined;
    };
  },
);
let listenHandler:
  | ((event: { payload: { sessionId?: string | null } }) => void)
  | undefined;

vi.mock("@tauri-apps/plugin-notification", () => ({
  isPermissionGranted,
  requestPermission,
  sendNotification,
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invoke(...args),
}));

vi.mock("@tauri-apps/api/event", () => ({
  listen: (...args: unknown[]) =>
    (listen as (...a: unknown[]) => unknown)(...args),
}));

describe("notifications wrapper (web / non-Tauri)", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.doMock("../runtime.ts", () => ({
      isTauriRuntime: () => false,
      tauriPlatform: () => null,
    }));
  });

  afterEach(() => {
    vi.doUnmock("../runtime.ts");
    vi.resetModules();
  });

  it("no-ops permission, send, and activation subscribe without throwing", async () => {
    const {
      ensureNotificationPermission,
      sendNotification: send,
      cancelNotificationsForSessions,
      onNotificationActivated,
    } = await import("./notifications.ts");

    await expect(ensureNotificationPermission()).resolves.toBe("unavailable");
    await expect(
      send({ title: "Painted Wolf Code", body: "Finished" }),
    ).resolves.toBeUndefined();
    await expect(
      cancelNotificationsForSessions(["gone-session"]),
    ).resolves.toBeUndefined();
    expect(() => onNotificationActivated(() => undefined)).not.toThrow();
    expect(isPermissionGranted).not.toHaveBeenCalled();
    expect(sendNotification).not.toHaveBeenCalled();
    expect(invoke).not.toHaveBeenCalled();
  });
});

describe("notifications wrapper (Tauri non-macOS plugin)", () => {
  beforeEach(() => {
    vi.resetModules();
    isPermissionGranted.mockReset();
    requestPermission.mockReset();
    sendNotification.mockReset();
    invoke.mockReset();
    isPermissionGranted.mockResolvedValue(false);
    requestPermission.mockResolvedValue("denied");
    vi.doMock("../runtime.ts", () => ({
      isTauriRuntime: () => true,
      tauriPlatform: () => "linux" as const,
    }));
  });

  afterEach(() => {
    vi.doUnmock("../runtime.ts");
    vi.resetModules();
  });

  it("maps permission check and lazy request", async () => {
    isPermissionGranted.mockResolvedValueOnce(false);
    const { ensureNotificationPermission } =
      await import("./notifications.ts");


    isPermissionGranted.mockResolvedValueOnce(false);
    requestPermission.mockResolvedValueOnce("granted");
    await expect(ensureNotificationPermission()).resolves.toBe("granted");
    expect(requestPermission).toHaveBeenCalledOnce();
  });

  it("distinguishes permission denial from unavailable permission state", async () => {
    const { ensureNotificationPermission } = await import("./notifications.ts");
    requestPermission.mockResolvedValueOnce("denied");
    await expect(ensureNotificationPermission()).resolves.toBe("denied");
    requestPermission.mockResolvedValueOnce("default");
    await expect(ensureNotificationPermission()).resolves.toBe("unavailable");
    isPermissionGranted.mockRejectedValueOnce(new Error("permission service unavailable"));
    await expect(ensureNotificationPermission()).resolves.toBe("unavailable");
  });

  it("sends only when permission is granted and never throws on plugin failure", async () => {
    isPermissionGranted.mockResolvedValue(true);
    const { sendNotification: send } = await import("./notifications.ts");

    await send({
      title: "Painted Wolf Code",
      body: "Needs your input",
      sessionId: "sess-1",
    });
    expect(sendNotification).toHaveBeenCalledWith({
      title: "Painted Wolf Code",
      body: "Needs your input",
    });

    sendNotification.mockImplementationOnce(() => {
      throw new Error("boom");
    });
    await expect(
      send({ title: "Painted Wolf Code", body: "Finished" }),
    ).resolves.toBeUndefined();
  });

  it("uses only supported desktop plugin operations", async () => {
    const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
    try {
      const { cancelNotificationsForSessions, onNotificationActivated } =
        await import("./notifications.ts");
      const activated = vi.fn();
      const unsubscribe = onNotificationActivated(activated);
      await cancelNotificationsForSessions(["sess-1"]);
      unsubscribe();
      expect(activated).not.toHaveBeenCalled();
      expect(invoke).not.toHaveBeenCalled();
      expect(debug).not.toHaveBeenCalled();
    } finally {
      debug.mockRestore();
    }
  });
});

describe("notifications wrapper (macOS UN bridge)", () => {
  beforeEach(() => {
    vi.resetModules();
    invoke.mockReset();
    listen.mockClear();
    listenHandler = undefined;
    sendNotification.mockClear();
    vi.doMock("../runtime.ts", () => ({
      isTauriRuntime: () => true,
      tauriPlatform: () => "macos" as const,
    }));
  });

  afterEach(() => {
    vi.doUnmock("../runtime.ts");
    vi.resetModules();
  });

  it("removes only the requested sessions through the native bridge", async () => {
    invoke.mockResolvedValue(undefined);
    const { cancelNotificationsForSessions } = await import("./notifications.ts");
    await cancelNotificationsForSessions([" sess-1 ", "sess-1", "", "sess-2"]);
    expect(invoke).toHaveBeenCalledExactlyOnceWith("den_notify_cancel_sessions", {
      sessionIds: ["sess-1", "sess-2"],
    });
    invoke.mockClear();
    await cancelNotificationsForSessions([" "]);
    expect(invoke).not.toHaveBeenCalled();
  });

  it("releases an activation listener that arrives after unsubscribe", async () => {
    let finish: ((unlisten: () => void) => void) | undefined;
    listen.mockImplementationOnce(() => new Promise((resolve) => {
      finish = resolve;
    }));
    const { onNotificationActivated } = await import("./notifications.ts");
    const unsubscribe = onNotificationActivated(vi.fn());
    await vi.waitFor(() => expect(finish).toBeDefined());
    unsubscribe();
    const unlisten = vi.fn();
    finish?.(unlisten);
    await vi.waitFor(() => expect(unlisten).toHaveBeenCalledOnce());
    unsubscribe();
    expect(unlisten).toHaveBeenCalledOnce();
  });

  it("maps UN auth states and sends via den_notify_send", async () => {
    invoke.mockImplementation(async (cmd: string) => {
      if (cmd === "den_notify_permission_state") return "authorized";
      if (cmd === "den_notify_send") return undefined;
      throw new Error(`unexpected ${cmd}`);
    });

    const {
      ensureNotificationPermission,
      sendNotification: send,
    } = await import("./notifications.ts");

    await expect(ensureNotificationPermission()).resolves.toBe("granted");
    await send({
      title: "Painted Wolf Code",
      body: "Finished — test notification",
      sessionId: "sess-mac",
    });
    expect(invoke).toHaveBeenCalledWith("den_notify_send", {
      title: "Painted Wolf Code",
      body: "Finished — test notification",
      sessionId: "sess-mac",
    });
    expect(sendNotification).not.toHaveBeenCalled();
  });

  it("preserves native denial without inventing denial on bridge failure", async () => {
    const { ensureNotificationPermission } = await import("./notifications.ts");
    invoke.mockResolvedValueOnce("denied");
    await expect(ensureNotificationPermission()).resolves.toBe("denied");
    expect(invoke).not.toHaveBeenCalledWith("den_notify_request_permission");
    invoke.mockRejectedValueOnce(new Error("notification service unavailable"));
    await expect(ensureNotificationPermission()).resolves.toBe("unavailable");
    invoke.mockResolvedValueOnce("not_determined").mockResolvedValueOnce("not_determined");
    await expect(ensureNotificationPermission()).resolves.toBe("unavailable");
  });

  it("requests UN permission when not determined", async () => {
    invoke.mockImplementation(async (cmd: string) => {
      if (cmd === "den_notify_permission_state") return "not_determined";
      if (cmd === "den_notify_request_permission") return "authorized";
      throw new Error(`unexpected ${cmd}`);
    });
    const { ensureNotificationPermission } = await import("./notifications.ts");
    await expect(ensureNotificationPermission()).resolves.toBe("granted");
    expect(invoke).toHaveBeenCalledWith("den_notify_request_permission");
  });

  it("reports unavailable and does not use the plugin outside a .app", async () => {
    invoke.mockImplementation(async (cmd: string) => {
      if (cmd === "den_notify_permission_state") return "unavailable";
      throw new Error(`unexpected ${cmd}`);
    });

    const {
      ensureNotificationPermission,
      sendNotification: send,
    } = await import("./notifications.ts");
    await expect(ensureNotificationPermission()).resolves.toBe("unavailable");
    await send({
      title: "Painted Wolf Code",
      body: "Finished — test notification",
    });
    expect(sendNotification).not.toHaveBeenCalled();
    expect(invoke).not.toHaveBeenCalledWith(
      "den_notify_send",
      expect.anything(),
    );
  });

  it("routes activation from notifications://activated events", async () => {
    invoke.mockImplementation(async (cmd: string) => {
      if (cmd === "den_notify_permission_state") return "authorized";
      throw new Error(`unexpected ${cmd}`);
    });
    const { onNotificationActivated } = await import("./notifications.ts");
    const seen: Array<string | undefined> = [];
    const unsub = onNotificationActivated((sessionId) => {
      seen.push(sessionId);
    });

    await vi.waitFor(() => {
      expect(listen).toHaveBeenCalled();
    });
    listenHandler?.({ payload: { sessionId: "sess-un" } });
    expect(seen).toEqual(["sess-un"]);
    unsub();
  });
});
