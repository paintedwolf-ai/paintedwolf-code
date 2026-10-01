import { describe, expect, it } from "vitest";
import {
  applyInvocationRecording,
  forgetInvocationRecordingSession,
  getRecordingForInvocation,
  resetInvocationRecordingStoreForTests,
} from "./invocation-recording-store.ts";

function recording(sessionId: string) {
  return {
    sessionId,
    artifactId: `artifact-${sessionId}`,
    assistantMessageId: "assistant-1",
    toolCallId: "call-1",
  };
}

describe("invocation-recording-store", () => {
  it("stores and returns recordings by invocation", () => {
    resetInvocationRecordingStoreForTests();
    applyInvocationRecording(recording("s1"));
    expect(
      getRecordingForInvocation("s1", "assistant-1", "call-1")?.artifactId,
    ).toBe("artifact-s1");
  });

  it("evicts the oldest session past the session cap", () => {
    resetInvocationRecordingStoreForTests();
    for (let i = 0; i < 70; i++) {
      applyInvocationRecording(recording(`s${i}`));
    }
    expect(
      getRecordingForInvocation("s0", "assistant-1", "call-1"),
    ).toBeUndefined();
    expect(
      getRecordingForInvocation("s69", "assistant-1", "call-1")?.artifactId,
    ).toBe("artifact-s69");
  });

  it("touch on write keeps a re-used session resident", () => {
    resetInvocationRecordingStoreForTests();
    applyInvocationRecording(recording("keep"));
    for (let i = 0; i < 63; i++) {
      applyInvocationRecording(recording(`s${i}`));
    }
    // Re-touch "keep", then push one more session over the cap.
    applyInvocationRecording(recording("keep"));
    applyInvocationRecording(recording("s-newest"));
    expect(
      getRecordingForInvocation("keep", "assistant-1", "call-1"),
    ).toBeDefined();
    expect(
      getRecordingForInvocation("s0", "assistant-1", "call-1"),
    ).toBeUndefined();
  });

  it("forgets a retired session", () => {
    resetInvocationRecordingStoreForTests();
    applyInvocationRecording(recording("gone"));
    forgetInvocationRecordingSession("gone");
    expect(
      getRecordingForInvocation("gone", "assistant-1", "call-1"),
    ).toBeUndefined();
  });
});
