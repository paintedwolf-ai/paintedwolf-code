import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import { lastWorkerToolActivity } from "./worker-activity.ts";

function assistantCall(
  id: string,
  name: string,
  args: Record<string, unknown>,
): Message {
  return {
    id,
    role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
    content: "",
    tool_calls: [{ id: `${id}-call`, name, args }],
    created_at: "2026-06-12T15:00:00Z",
  };
}

function toolResult(id: string, content: string): Message {
  return { id, role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const, content, created_at: "2026-06-12T15:00:01Z" };
}

describe("lastWorkerToolActivity", () => {
  it("renders the newest call as tool · chicklet title", () => {
    const messages = [
      assistantCall("m1", "grep", { pattern: "portal", path: "index.html" }),
      toolResult("m2", '{"matches":[]}'),
      assistantCall("m3", "read", { path: "index.html", ranges: [] }),
    ];
    expect(lastWorkerToolActivity(messages)).toBe("read · index.html");
  });

  it("never surfaces result JSON payloads", () => {
    const messages = [
      assistantCall("m1", "read", { path: "index.html" }),
      toolResult(
        "m2",
        '{"clamped":"served 2 of 4 requested ranges (800 of 1600 lines)"}',
      ),
    ];
    expect(lastWorkerToolActivity(messages)).toBe("read · index.html");
  });

  it("falls back to the bare tool name when no title resolves", () => {
    const messages = [assistantCall("m1", "git_branches", {})];
    expect(lastWorkerToolActivity(messages)).toBe("git_branches");
  });

  it("shows agent and brief line for task calls", () => {
    const messages = [
      assistantCall("m1", "task", {
        agent_type: "implementer",
        brief: {
          goal: "Transform this 3D Checkers game\nKeep the rules intact.",
          done_when: ["Return the completed transformation."],
        },
      }),
    ];
    expect(lastWorkerToolActivity(messages)).toBe(
      "task · implementer · Transform this 3D Checkers game",
    );
  });

  it("returns null when no tool calls exist", () => {
    const messages = [toolResult("m1", '{"ok":true}')];
    expect(lastWorkerToolActivity(messages)).toBeNull();
  });
});
