// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

/** Tests registration inside the desktop webview. */

const isTauriRuntime = vi.fn(() => true);
const onDragDropEvent = vi.fn();

vi.mock("../runtime.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../runtime.ts")>();
  return {
    ...actual,
    isTauriRuntime: () => isTauriRuntime(),
  };
});

vi.mock("@tauri-apps/api/webview", () => ({
  getCurrentWebview: () => ({
    onDragDropEvent: (cb: (event: unknown) => void) => onDragDropEvent(cb),
  }),
}));

const { registerChatDrop, resetChatDropSubscriptionForTests } = await import(
  "./file-drop.ts"
);

/** Hand the module a fresh native subscription and return its dispatch callback. */
async function armNativeSubscription(): Promise<(event: unknown) => void> {
  await vi.waitFor(() => expect(onDragDropEvent).toHaveBeenCalled());
  return onDragDropEvent.mock.calls[0]![0] as (event: unknown) => void;
}

function dropEvent(paths: string[]) {
  return {
    payload: { type: "drop", paths, position: { x: 1, y: 2 } },
  };
}

function handlers() {
  return {
    onDragActive: vi.fn(),
    onDrop: vi.fn(),
  };
}

describe("registerChatDrop — native subscription", () => {
  beforeEach(() => {
    isTauriRuntime.mockReturnValue(true);
    onDragDropEvent.mockReset();
    onDragDropEvent.mockResolvedValue(() => {});
    resetChatDropSubscriptionForTests();
  });

  it("subscribes to the webview once across many register/release cycles", async () => {
    for (let i = 0; i < 5; i++) {
      const release = registerChatDrop(handlers());
      release();
    }
    await armNativeSubscription();
    // All mounts share one native listener.
    expect(onDragDropEvent).toHaveBeenCalledTimes(1);
  });

  it("routes drops to the most recent claimant", async () => {
    const first = handlers();
    const second = handlers();
    registerChatDrop(first);
    registerChatDrop(second);
    const dispatch = await armNativeSubscription();

    dispatch(dropEvent(["/proj/a.txt"]));

    expect(second.onDrop).toHaveBeenCalledTimes(1);
    expect(first.onDrop).not.toHaveBeenCalled();
  });

  it("an earlier release does not disarm the new chat", async () => {
    const outgoing = handlers();
    const incoming = handlers();
    const releaseOutgoing = registerChatDrop(outgoing);
    // Concurrent residents can release in either order.
    registerChatDrop(incoming);
    releaseOutgoing();

    const dispatch = await armNativeSubscription();
    dispatch(dropEvent(["/proj/a.txt"]));

    expect(incoming.onDrop).toHaveBeenCalledTimes(1);
    expect(outgoing.onDrop).not.toHaveBeenCalled();
  });

  it("drops are ignored while no chat holds the claim", async () => {
    const only = handlers();
    const release = registerChatDrop(only);
    const dispatch = await armNativeSubscription();
    release();

    expect(() => dispatch(dropEvent(["/proj/a.txt"]))).not.toThrow();
    expect(only.onDrop).not.toHaveBeenCalled();
  });

  it("maps a native drop payload to absolute-path items", async () => {
    const target = handlers();
    registerChatDrop(target);
    const dispatch = await armNativeSubscription();

    dispatch(dropEvent(["/proj/a.txt", "/proj/b.png"]));

    expect(target.onDragActive).toHaveBeenLastCalledWith(false);
    expect(target.onDrop).toHaveBeenCalledWith({
      items: [
        { source: "path", absolutePath: "/proj/a.txt" },
        { source: "path", absolutePath: "/proj/b.png" },
      ],
      position: { x: 1, y: 2 },
    });
  });

  it("web runtime attaches HTML5 listeners to the caller's target instead", () => {
    isTauriRuntime.mockReturnValue(false);
    const target = document.createElement("div");
    const add = vi.spyOn(target, "addEventListener");
    const remove = vi.spyOn(target, "removeEventListener");

    const release = registerChatDrop({ ...handlers(), htmlTarget: target });
    expect(add).toHaveBeenCalled();
    expect(onDragDropEvent).not.toHaveBeenCalled();

    release();
    expect(remove).toHaveBeenCalledTimes(add.mock.calls.length);
  });
});
