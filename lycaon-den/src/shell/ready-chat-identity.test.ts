import { createMemo, createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { stabilizeActiveChat, type ActiveChat } from "./stage-scope.ts";

describe("stabilizeActiveChat", () => {
  it("reuses the same object when ids are unchanged", () => {
    createRoot((dispose) => {
      const [tick, setTick] = createSignal(0);
      const readyChat = createMemo((prev: ActiveChat | null | undefined) => {
        tick();
        return stabilizeActiveChat(prev, "proj-a", "sess-a", true);
      });
      const first = readyChat();
      setTick(1);
      expect(readyChat()).toBe(first);
      dispose();
    });
  });

  it("allocates a new object when the session id changes", () => {
    createRoot((dispose) => {
      const [sessionId, setSessionId] = createSignal("sess-a");
      const readyChat = createMemo((prev: ActiveChat | null | undefined) =>
        stabilizeActiveChat(prev, "proj-a", sessionId(), true),
      );
      const first = readyChat();
      setSessionId("sess-b");
      const second = readyChat();
      expect(second).not.toBe(first);
      expect(second).toEqual({ projectId: "proj-a", sessionId: "sess-b" });
      dispose();
    });
  });

  it("returns null when not ready", () => {
    expect(stabilizeActiveChat(null, "proj-a", "sess-a", false)).toBeNull();
  });
});
