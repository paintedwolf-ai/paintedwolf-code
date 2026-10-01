import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const isTauriRuntime = vi.fn(() => true);
const invoke = vi.fn(async (_cmd: string, _args: unknown) => true);
const buildAppMenuSpec = vi.fn();

vi.mock("../runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../runtime.ts")>()),
  isTauriRuntime: () => isTauriRuntime(),
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (cmd: string, args: unknown) => invoke(cmd, args),
}));

vi.mock("../../shortcuts/app-menu-model.ts", () => ({
  buildAppMenuSpec: (opts: unknown) => buildAppMenuSpec(opts),
}));

let menuListener: ((event: { payload: unknown }) => void) | null = null;
vi.mock("../windows/window-channel.ts", () => ({
  listenHostEvent: async (
    _event: string,
    handler: (event: { payload: unknown }) => void,
  ) => {
    menuListener = handler;
    return () => {
      menuListener = null;
    };
  },
}));

const {
  attachAppMenuCommands,
  invalidateAppMenuProjection,
  syncAppMenu,
  resetAppMenuForTests,
} = await import("./app-menu.ts");
const { seedStockFrame, stockBindingId, stockId } = await import(
  "../../contributions/stock-frame-test.ts"
);
const { resetContributionStoreForTest } = await import("../../contributions/contribution-store.ts");
const { claimShortcutBoundary } = await import("../../shortcuts/dispatcher.ts");

const specA = [{ menu: "file", title: "File", groups: [] }];
const specB = [{ menu: "file", title: "Fichier", groups: [] }];

describe("syncAppMenu", () => {
  beforeEach(() => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockReset();
    invoke.mockResolvedValue(true);
    buildAppMenuSpec.mockReset();
    buildAppMenuSpec.mockReturnValue(specA);
    resetAppMenuForTests();
  });

  it("pushes a changed spec", async () => {
    await syncAppMenu(null);
    expect(invoke).toHaveBeenCalledTimes(1);
    expect(invoke).toHaveBeenCalledWith("set_app_menu", { sections: specA });
  });

  it("keeps declaration metadata out of the host payload", async () => {
    buildAppMenuSpec.mockReturnValue([
      {
        menu: "file",
        title: "File",
        groups: [[{
          id: "command",
          title: "Command",
          accelerator: "CmdOrCtrl+K",
          bindingId: "binding",
          binding: "Mod+K",
          enabled: true,
        }]],
      },
    ]);
    await syncAppMenu(null);
    expect(invoke).toHaveBeenCalledWith("set_app_menu", {
      sections: [{
        menu: "file",
        title: "File",
        groups: [[{
          id: "command",
          title: "Command",
          accelerator: "CmdOrCtrl+K",
          enabled: true,
        }]],
      }],
    });
  });

  it("does not re-push an unchanged spec", async () => {
    await syncAppMenu(null);
    await syncAppMenu(null);
    await syncAppMenu(null);
    expect(invoke).toHaveBeenCalledTimes(1);
  });

  it("reasserts the projection when this webview regains focus", async () => {
    await syncAppMenu(null);
    invalidateAppMenuProjection();
    await syncAppMenu(null);
    expect(invoke).toHaveBeenCalledTimes(2);
  });

  it("does not cache a projection the host rejected after focus moved", async () => {
    invoke.mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    await syncAppMenu(null);
    await syncAppMenu(null);
    expect(invoke).toHaveBeenCalledTimes(2);
  });

  it("collapses a same-tick burst into one push of the newest spec", async () => {
    const first = syncAppMenu(null);
    buildAppMenuSpec.mockReturnValue(specB);
    const second = syncAppMenu(null);
    const third = syncAppMenu(null);
    await Promise.all([first, second, third]);

    expect(invoke).toHaveBeenCalledTimes(1);
    expect(invoke).toHaveBeenCalledWith("set_app_menu", { sections: specB });
  });

  it("pushes again when the spec changes after a completed push", async () => {
    await syncAppMenu(null);
    buildAppMenuSpec.mockReturnValue(specB);
    await syncAppMenu(null);
    expect(invoke).toHaveBeenCalledTimes(2);
    expect(invoke).toHaveBeenLastCalledWith("set_app_menu", { sections: specB });
  });

  it("a spec queued mid-flight still lands", async () => {
    let release!: () => void;
    invoke.mockImplementationOnce(
      () => new Promise<boolean>((r) => { release = () => r(true); }),
    );

    const first = syncAppMenu(null);
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledTimes(1));

    buildAppMenuSpec.mockReturnValue(specB);
    const second = syncAppMenu(null);
    release();
    await Promise.all([first, second]);

    expect(invoke).toHaveBeenCalledTimes(2);
    expect(invoke).toHaveBeenLastCalledWith("set_app_menu", { sections: specB });
  });

  it("a failed push is not remembered as pushed", async () => {
    invoke.mockRejectedValueOnce(new Error("denied"));
    const error = vi.spyOn(console, "error").mockImplementation(() => {});

    await syncAppMenu(null);
    expect(error).toHaveBeenCalled();

    await syncAppMenu(null);
    expect(invoke).toHaveBeenCalledTimes(2);
    error.mockRestore();
  });

  it("retries a projection queued behind a failed push", async () => {
    let reject!: (reason: Error) => void;
    invoke.mockImplementationOnce(
      () =>
        new Promise<boolean>((_, fail) => {
          reject = fail;
        }),
    );
    const error = vi.spyOn(console, "error").mockImplementation(() => {});

    const first = syncAppMenu(null);
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledTimes(1));
    buildAppMenuSpec.mockReturnValue(specB);
    const queued = syncAppMenu(null);
    reject(new Error("denied"));
    await Promise.all([first, queued]);

    await syncAppMenu(null);
    expect(invoke).toHaveBeenCalledTimes(2);
    expect(invoke).toHaveBeenLastCalledWith("set_app_menu", { sections: specB });
    error.mockRestore();
  });

  it("hands the user's overrides to the model", async () => {
    const overrides = {
      [stockBindingId("session-new")]: "Mod+Alt+T",
    };
    await syncAppMenu(overrides);
    expect(buildAppMenuSpec).toHaveBeenCalledWith({ overrides });
  });

  it("is a no-op off Tauri", async () => {
    isTauriRuntime.mockReturnValue(false);
    await syncAppMenu(null);
    expect(invoke).not.toHaveBeenCalled();
    expect(buildAppMenuSpec).not.toHaveBeenCalled();
  });

  it("a build failure never throws into the shell", async () => {
    buildAppMenuSpec.mockImplementation(() => {
      throw new Error("bad spec");
    });
    const debug = vi.spyOn(console, "debug").mockImplementation(() => {});
    await expect(syncAppMenu(null)).resolves.toBeUndefined();
    expect(invoke).not.toHaveBeenCalled();
    debug.mockRestore();
  });
});

describe("attachAppMenuCommands", () => {
  const activate = vi.fn();

  beforeEach(() => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockReset();
    invoke.mockResolvedValue(true);
    buildAppMenuSpec.mockReset();
    buildAppMenuSpec.mockReturnValue(specA);
    resetAppMenuForTests();
    activate.mockClear();
    menuListener = null;
    seedStockFrame();
  });

  afterEach(() => {
    resetContributionStoreForTest();
  });

  it("dispatches the frame command the clicked item names", async () => {
    const stop = attachAppMenuCommands(activate);
    await Promise.resolve();
    menuListener?.({ payload: stockId("session-new") });
    expect(activate).toHaveBeenCalledWith(stockId("session-new"));
    stop();
  });

  it("ignores an id the hydrated frame does not carry", async () => {
    const stop = attachAppMenuCommands(activate);
    await Promise.resolve();
    menuListener?.({ payload: "acme/pack:not-in-frame" });
    menuListener?.({ payload: 42 });
    expect(activate).not.toHaveBeenCalled();
    stop();
  });

  it("stops native accelerators at a modal shortcut boundary", async () => {
    buildAppMenuSpec.mockReturnValue([
      {
        menu: "file",
        title: "File",
        groups: [[{
          id: stockId("session-new"),
          title: "New chat",
          accelerator: "CmdOrCtrl+N",
          bindingId: stockBindingId("session-new"),
          binding: "Mod+N",
          enabled: true,
        }]],
      },
    ]);
    await syncAppMenu(null);
    const release = claimShortcutBoundary();
    const stop = attachAppMenuCommands(activate);
    await Promise.resolve();

    menuListener?.({ payload: stockId("session-new") });
    expect(activate).not.toHaveBeenCalled();

    release();
    stop();
  });

  it("re-checks the declaration condition when an accelerator fires", async () => {
    seedStockFrame({ projectOpen: false });
    buildAppMenuSpec.mockReturnValue([
      {
        menu: "file",
        title: "File",
        groups: [[{
          id: stockId("session-new"),
          title: "New chat",
          accelerator: "CmdOrCtrl+N",
          bindingId: stockBindingId("session-new"),
          binding: "Mod+N",
          enabled: true,
        }]],
      },
    ]);
    await syncAppMenu(null);
    const stop = attachAppMenuCommands(activate);
    await Promise.resolve();

    menuListener?.({ payload: stockId("session-new") });
    expect(activate).not.toHaveBeenCalled();

    seedStockFrame({ projectOpen: true });
    menuListener?.({ payload: stockId("session-new") });
    expect(activate).toHaveBeenCalledOnce();
    stop();
  });
});
