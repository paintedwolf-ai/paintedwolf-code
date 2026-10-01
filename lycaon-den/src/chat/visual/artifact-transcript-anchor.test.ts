import { describe, expect, it } from "vitest";
import { artifactTranscriptAnchor } from "./artifact-transcript-anchor.ts";

describe("artifactTranscriptAnchor", () => {
  it("prefers the tool call over the origin message", () => {
    // A tool result's message id is not a key any row carries.
    expect(
      artifactTranscriptAnchor({
        tool_call_id: "call-7",
        origin_message_id: "msg-tool-result",
      }),
    ).toEqual({ chicklet: "tool", anchorId: "call-7" });
  });

  it("falls back to the message row for an artifact with no tool call", () => {
    expect(
      artifactTranscriptAnchor({ origin_message_id: "msg-upload" }),
    ).toEqual({ chicklet: "message", anchorId: "msg-upload" });
  });

  it("reports no anchor when neither link is set", () => {
    expect(artifactTranscriptAnchor({})).toBeNull();
    expect(artifactTranscriptAnchor(null)).toBeNull();
  });

  it("treats blank ids as absent", () => {
    expect(
      artifactTranscriptAnchor({
        tool_call_id: "   ",
        origin_message_id: "msg-1",
      }),
    ).toEqual({ chicklet: "message", anchorId: "msg-1" });
    expect(
      artifactTranscriptAnchor({ tool_call_id: "", origin_message_id: " " }),
    ).toBeNull();
  });
});
