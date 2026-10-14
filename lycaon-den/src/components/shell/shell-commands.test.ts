// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const focusRegion = vi.hoisted(() => vi.fn(() => true));
vi.mock("../../shortcuts/focus-region.ts", () => ({
  focusRegion,
  cycleFocusRegion: vi.fn(),
}));
const invoke = vi.hoisted(() => vi.fn(async (_command: string) => undefined));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));
const tauri = vi.hoisted(() => ({ runtime: false }));
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  isTauriRuntime: () => tauri.runtime,
}));

import { invokeCommand, resetDispatcherForTests } from "../../shortcuts/dispatcher.ts";
import { registerShellCommands } from "./shell-commands.ts";

type Deps = Parameters<typeof registerShellCommands>[0];

function mountCommands(overrides: Partial<Deps>): () => void {
  const base: Partial<Deps> = {
    activeChat: () => ({ projectId: "p1", sessionId: "s1" }) as ReturnType<Deps["activeChat"]>,
    readyChat: () => null,
    splitLive: () => false,
    conversationInLayout: () => true,
    isPeerWindow: false,
    sessionPeer: () => undefined,
    isThisSessionWindow: () => false,
    raiseSessionWindow: vi.fn(),
  };
  const deps = new Proxy({ ...base, ...overrides } as Deps, {
    get: (target, key) => (key in target ? target[key as keyof Deps] : vi.fn()),
  });
  let dispose!: () => void;
  createRoot((d) => {
    dispose = d;
    registerShellCommands(deps);
  });
  return dispose;
}

describe("go.chat", () => {
  let dispose: (() => void) | undefined;
  beforeEach(() => focusRegion.mockClear());
  afterEach(() => {
    dispose?.();
    resetDispatcherForTests();
  });

  it("raises the chat's peer window from another window", () => {
    const raiseSessionWindow = vi.fn();
    dispose = mountCommands({
      sessionPeer: () => ({ label: "session:s1:2" }) as ReturnType<Deps["sessionPeer"]>,
      raiseSessionWindow,
    });
    invokeCommand("go.chat");
    expect(raiseSessionWindow).toHaveBeenCalledWith("s1");
    expect(focusRegion).not.toHaveBeenCalled();
  });

  it("focuses the composer in the chat's own peer window", () => {
    const raiseSessionWindow = vi.fn();
    dispose = mountCommands({
      isPeerWindow: true,
      sessionPeer: () => ({ label: "session:s1:2" }) as ReturnType<Deps["sessionPeer"]>,
      isThisSessionWindow: (id) => id === "s1",
      raiseSessionWindow,
    });
    invokeCommand("go.chat");
    expect(raiseSessionWindow).not.toHaveBeenCalled();
    expect(focusRegion).toHaveBeenCalledWith("composer");
  });
});

describe("layout.swapColumns", () => {
  let dispose: (() => void) | undefined;
  afterEach(() => {
    dispose?.();
    resetDispatcherForTests();
  });

  it("changes split order only while a split is live", async () => {
    const { resetLayoutForTests } = await import("../../layout/layout-test-support.ts");
    const { splitOrderPref, workspaceOrientationPref } = await import("../../shell/workspace-layout.ts");
    resetLayoutForTests();
    dispose = mountCommands({ splitLive: () => false });
    invokeCommand("layout.swapColumns");
    expect(splitOrderPref()).toBe("context-first");
    dispose();
    dispose = mountCommands({ splitLive: () => true });
    invokeCommand("layout.swapColumns");
    expect(splitOrderPref()).toBe("chat-first");
    expect(workspaceOrientationPref()).toBe("standard");
    invokeCommand("layout.swapColumns");
    expect(splitOrderPref()).toBe("context-first");
    resetLayoutForTests();
  });
});

describe("app menu and quit", () => {
  let dispose: (() => void) | undefined;
  afterEach(async () => {
    dispose?.();
    resetDispatcherForTests();
    tauri.runtime = false;
    const { setShortcutPlatformForTests } = await import("../../shortcuts/platform.ts");
    setShortcutPlatformForTests(null);
  });

  it("asks the host to quit only from the desktop runtime", async () => {
    dispose = mountCommands({});
    invokeCommand("app.quit");
    await Promise.resolve();
    expect(invoke).not.toHaveBeenCalled();
    tauri.runtime = true;
    invokeCommand("app.quit");
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledWith("request_quit"));
  });

  it("opens the in-window app menu on Linux and declines elsewhere", async () => {
    const { setShortcutPlatformForTests } = await import("../../shortcuts/platform.ts");
    const { appMenuOpenRequests } = await import("../../platform/desktop/in-window-app-menu.ts");
    dispose = mountCommands({});
    setShortcutPlatformForTests("macos");
    const before = appMenuOpenRequests();
    expect(invokeCommand("appMenu.open")).toBe("declined");
    expect(appMenuOpenRequests()).toBe(before);
    setShortcutPlatformForTests("linux");
    expect(invokeCommand("appMenu.open")).toBe("ran");
    expect(appMenuOpenRequests()).toBe(before + 1);
  });
});
