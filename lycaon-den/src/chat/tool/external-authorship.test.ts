import { describe, expect, it } from "vitest";
import { toolPartFromCall } from "./tool-part-model.ts";
import type { Message, ToolCall } from "../../api/types.ts";

/** External authorship comes from the host provenance stamp. */
function call(tool: string): ToolCall {
  return { id: "c1", name: tool, args: {} };
}

function result(origin: Message["origin"]): Message {
  return {
    id: "t1",
    role: "tool",
    origin,
    authority: "none",
    trust_tier: "untrusted",
    content: "body",
    created_at: "t",
    tool_result: { tool_call_id: "c1", content: "body" },
  };
}

describe("tool part external authorship", () => {
  it("marks a result the host stamped as retrieval", () => {
    expect(
      toolPartFromCall(call("fetch_url"), result("retrieval"), "a1")
        .externallyAuthored,
    ).toBe(true);
  });

  it("leaves a locally produced tool result unmarked", () => {
    expect(
      toolPartFromCall(call("read"), result("tool"), "a1").externallyAuthored,
    ).toBe(false);
  });

  it("follows the stamp rather than the tool name", () => {
    expect(
      toolPartFromCall(call("mcp.acme.deploy"), result("tool"), "a1")
        .externallyAuthored,
    ).toBe(false);
    expect(
      toolPartFromCall(call("anything"), result("retrieval"), "a1")
        .externallyAuthored,
    ).toBe(true);
  });

  it("treats a missing result as not externally authored", () => {
    expect(
      toolPartFromCall(call("fetch_url"), undefined, "a1").externallyAuthored,
    ).toBe(false);
  });
});
