import { createRoot } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createQueueController } from "./queue-controller.ts";
import type { AppStore } from "../../store/app-state-model.ts";

const mocks = vi.hoisted(() => ({
  setQueueHold: vi.fn(async () => undefined),
  fireQueueItemNow: vi.fn(async () => undefined),
  removeQueueItems: vi.fn(async () => undefined),
  linkQueueItems: vi.fn(async () => undefined),
  unlinkQueueItems: vi.fn(async () => undefined),
  reorderQueue: vi.fn(async () => undefined),
  updateQueueItem: vi.fn(async () => undefined),
  sendQueue: vi.fn(async () => undefined),
  cancelQueueSend: vi.fn(async () => undefined),
}));

vi.mock("../../chat/actions/queue-actions.ts", () => mocks);

vi.mock("../../platform/connection/app-connection.ts", () => ({
  getLycaonClient: () => ({ id: "client" }),
}));

function makeController(hold = false, onSend?: () => void) {
  return createQueueController({
    appStore: {
      state: { queueDraft: { queue_items: [], hold, sending: false, revision: 1 } },
    } as unknown as AppStore,
    sessionId: () => "sess-1",
    onSend,
  });
}

describe("createQueueController", () => {
  it("beginArrange holds and endArrange releases when not paused", () => {
    createRoot((dispose) => {
      const queue = makeController(false);
      queue.beginArrange();
      expect(queue.arranging()).toBe(true);
      expect(mocks.setQueueHold).toHaveBeenLastCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
        true,
      );
      queue.endArrange();
      expect(queue.arranging()).toBe(false);
      expect(mocks.setQueueHold).toHaveBeenLastCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
        false,
      );
      dispose();
    });
  });

  it("an explicit pause survives an arrange cycle", () => {
    createRoot((dispose) => {
      mocks.setQueueHold.mockClear();
      const queue = makeController(true);
      queue.beginArrange();
      queue.endArrange();
      expect(mocks.setQueueHold).not.toHaveBeenCalled();
      dispose();
    });
  });

  it("nested arranges release only once, on the last end", () => {
    createRoot((dispose) => {
      mocks.setQueueHold.mockClear();
      const queue = makeController(false);
      queue.beginArrange();
      queue.beginArrange();
      queue.endArrange();
      expect(queue.arranging()).toBe(true);
      expect(mocks.setQueueHold).toHaveBeenCalledTimes(1);
      queue.endArrange();
      expect(queue.arranging()).toBe(false);
      expect(mocks.setQueueHold).toHaveBeenLastCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
        false,
      );
      dispose();
    });
  });

  it("releases a live arrange-hold on teardown", () => {
    let queue!: ReturnType<typeof makeController>;
    const dispose = createRoot((d) => {
      queue = makeController(false);
      return d;
    });
    queue.beginArrange();
    mocks.setQueueHold.mockClear();
    dispose();
    expect(mocks.setQueueHold).toHaveBeenCalledWith(
      expect.anything(),
      expect.anything(),
      "sess-1",
      false,
    );
  });

  it("setPaused writes the flag and rebases the arrange restore target", () => {
    createRoot((dispose) => {
      const queue = makeController(false);
      queue.beginArrange();
      queue.setPaused(true);
      mocks.setQueueHold.mockClear();
      queue.endArrange();
      expect(mocks.setQueueHold).not.toHaveBeenCalled();
      dispose();
    });
  });

  it("send reports intent once before reservation, and cancel does not report it", () => {
    createRoot((dispose) => {
      const onSend = vi.fn();
      const queue = makeController(false, onSend);
      queue.send();
      expect(onSend).toHaveBeenCalledTimes(1);
      queue.cancelSend();
      expect(onSend).toHaveBeenCalledTimes(1);
      dispose();
    });
  });

  it("fireNow reports the explicit user send before posting", () => {
    createRoot((dispose) => {
      const onSend = vi.fn();
      const queue = makeController(false, onSend);
      queue.fireNow("a");
      expect(onSend).toHaveBeenCalledTimes(1);
      expect(mocks.fireQueueItemNow).toHaveBeenCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
        "a",
      );
      dispose();
    });
  });

  it("routes item actions to the queue API", () => {
    createRoot((dispose) => {
      const queue = makeController(false);
      queue.fireNow("a");
      expect(mocks.fireQueueItemNow).toHaveBeenCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
        "a",
      );
      queue.remove("a");
      expect(mocks.removeQueueItems).toHaveBeenCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
        ["a"],
      );
      queue.reorder(["b", "a"]);
      expect(mocks.reorderQueue).toHaveBeenCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
        ["b", "a"],
      );
      queue.update("a", "edited");
      expect(mocks.updateQueueItem).toHaveBeenCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
        "a",
        "edited",
      );
      queue.send();
      expect(mocks.sendQueue).toHaveBeenCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
      );
      queue.cancelSend();
      expect(mocks.cancelQueueSend).toHaveBeenCalledWith(
        expect.anything(),
        expect.anything(),
        "sess-1",
      );
      dispose();
    });
  });
});
