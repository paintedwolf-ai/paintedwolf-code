import { describe, expect, it } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import type { Message, TurnLoad, TurnLoadMatchBy } from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { TurnLoadIndexProvider } from "./turn-load-context.tsx";
import { ToolPartCard } from "./ToolPartCard.tsx";

const messages: Message[] = [
  { id: "u1", ord: 1, seq: 1, role: "user", origin: "user", authority: "user", trust_tier: "trusted", content: "build a container", created_at: "t" },
];

function part(tool: "request_tools" | "skills_read"): ToolPartView {
  return {
    id: `a1:${tool}`, toolCallId: "call-1", assistantMessageId: "a1", messageId: "m1",
    tool, kind: tool === "skills_read" ? "skill" : "generic", status: "completed",
    args: { need: "build a Docker image" },
    output: tool === "request_tools" ? JSON.stringify({ loaded: ["command"], note: "Loaded schemas are on the next model call." }) : "Skill: work-with-containers\n",
    error: null,
    ...(tool === "skills_read" ? { skill: {
      name: "work-with-containers", description: "Build Docker images.", instructions: "Build the image.", dir: "/skills/work-with-containers",
    } } : {}),
  };
}

function renderResolved(tool: "request_tools" | "skills_read", by: TurnLoadMatchBy) {
  const load: TurnLoad = {
    session_id: "s1", trigger: tool === "skills_read" ? "lookup" : "request",
    opening_message_id: "u1", tool_call_id: "call-1", abstained: false, elapsed_ms: 20,
    floor: [], tools: [], match: { need: "build a Docker image", by, names: [tool === "skills_read" ? "work-with-containers" : "command"] },
  };
  const view = render(() => (
    <TurnLoadIndexProvider messages={() => messages} turnLoads={() => ({ u1: [load] })}>
      <ToolPartCard part={part(tool)} layout="chat" sessionId="s1" />
    </TurnLoadIndexProvider>
  ));
  fireEvent.click(view.container.querySelector("summary")!);
  return view;
}

describe("tool resolution provenance", () => {
  it("shows Local AI in the loaded tools card", () => {
    const view = renderResolved("request_tools", "engine");
    expect(view.getByTestId("tool-resolution-facts").textContent).toBe("Loaded byLocal AI");
    expect(view.getByTestId("tool-resolution-method").textContent).toBe("Local AI");
    expect(view.container.textContent).toContain("Agent requested:");
    expect(view.container.textContent).toContain("command");
  });

  it("shows Local AI in the skill card", () => {
    const view = renderResolved("skills_read", "engine");
    expect(view.getByTestId("tool-resolution-method").textContent).toBe("Local AI");
    expect(view.getByTestId("skill-card-name").textContent).toBe("work-with-containers");
  });

  it("labels fallback selection by its actual method", () => {
    expect(renderResolved("request_tools", "name").getByTestId("tool-resolution-method").textContent).toBe("Exact name");
  });
});

it.each(["request_tools", "skills_read"] as const)("does not label an unavailable %s ranking as no match", (tool) => {
  const toolPart = part(tool);
  delete toolPart.skill;
  toolPart.output = JSON.stringify({ discovery: {
    status: "ranking_unavailable", failure: "timeout",
    entries: [{ name: "available-entry", description: "A useful procedure" }], total: 1,
  } });
  const load: TurnLoad = {
    session_id: "s1", trigger: tool === "skills_read" ? "lookup" : "request",
    opening_message_id: "u1", tool_call_id: "call-1", abstained: true, elapsed_ms: 10000,
    floor: [], tools: [], match: { need: "build a Docker image", by: "none", names: [] },
  };
  const view = render(() => (
    <TurnLoadIndexProvider messages={() => messages} turnLoads={() => ({ u1: [load] })}>
      <ToolPartCard part={toolPart} layout="chat" sessionId="s1" />
    </TurnLoadIndexProvider>
  ));
  fireEvent.click(view.container.querySelector("summary")!);
  expect(view.queryByTestId("tool-resolution-method")).toBeNull();
  expect(view.queryByTestId("skill-card-body")).toBeNull();
  expect(view.container.textContent).toContain("Local AI timed out");
  expect(view.container.textContent).toContain("available-entry");
});
