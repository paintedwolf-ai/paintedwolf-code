import { describe, expect, it, vi } from "vitest";
import {
  registerApprovalRevealSink,
  revealApprovalOrigin,
} from "./approval-reveal.ts";

describe("revealApprovalOrigin", () => {
  it("delivers normalized origin data to the registered shell sink", () => {
    const sink = vi.fn();
    const unregister = registerApprovalRevealSink(sink);
    try {
      revealApprovalOrigin({ toolCallId: " call-1 ", sessionId: " session-1 " });

      expect(sink).toHaveBeenCalledOnce();
      expect(sink).toHaveBeenCalledWith({ toolCallId: "call-1", sessionId: "session-1" });
    } finally {
      unregister();
    }
  });

  it("does not reveal a blank tool call id", () => {
    const sink = vi.fn();
    const unregister = registerApprovalRevealSink(sink);
    try {
      revealApprovalOrigin({ toolCallId: "   ", sessionId: "session-1" });

      expect(sink).not.toHaveBeenCalled();
    } finally {
      unregister();
    }
  });
});
