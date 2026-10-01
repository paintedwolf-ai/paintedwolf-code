// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const focusRegion = vi.hoisted(() => vi.fn(() => true));
vi.mock("../../shortcuts/focus-region.ts", () => ({
  focusRegion,
  cycleFocusRegion: vi.fn(),
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
    const { resetLayoutStoreForTests, splitOrderPref, workspaceOrientationPref } = await import("../../shell/layout-store.ts");
    resetLayoutStoreForTests();
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
    resetLayoutStoreForTests();
  });
});
