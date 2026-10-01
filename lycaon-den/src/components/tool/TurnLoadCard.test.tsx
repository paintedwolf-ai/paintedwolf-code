import { describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import type { TurnLoad } from "../../api/types.ts";
import type { TurnLoadRow } from "../../chat/turnload/turn-load-rows.ts";
import { TurnLoadCard, turnLoadToolPart } from "./TurnLoadCard.tsx";

const engine = { name: "Bialy", model: "mmbert-base", head: "turn-load", label: "Bialy/mmbert-base#turn-load" };
const load: TurnLoad = {
  session_id: "s1", trigger: "turn", opening_message_id: "u1", abstained: false, elapsed_ms: 438, engine,
  kind: { value: "change", confidence: 0.86 },
  floor: ["grep", "read"],
  tools: [{ tool: "git_commit", source: "predicted", p: 0.88, carried: false }, { tool: "git_status", source: "predicted", p: 0.91, carried: false }],
  guides: { rendered: 6, omitted: 9 },
};
const toolsRow: TurnLoadRow = { key: "turn-load:u1:turn::tools", role: "tools", load, assistantMessageId: "a1", anchorMessageId: "a1", sub: 8 };

describe("TurnLoadCard", () => {
  it("presents the decision as the engine's tool row", () => {
    const part = turnLoadToolPart(toolsRow);
    expect(part.tool).toBe("Bialy");
    expect(part.title).toBe("git_commit, git_status");
    expect(part.completion).toEqual({ operation: "offered", state: "438 ms" });
    expect(part.status).toBe("completed");
  });

  it("renders the tools row with its decision in the body", () => {
    const { getByTestId } = render(() => <TurnLoadCard row={toolsRow} layout="chat" />);
    const card = getByTestId("turn-load-card") as HTMLDetailsElement;
    expect(card.dataset.tool).toBe("Bialy");
    expect(card.textContent).toContain("Local AI");
    expect(card.textContent).toContain("git_commit, git_status");
    expect(card.textContent).toContain("offered · 438 ms");
    card.open = true;
    card.dispatchEvent(new Event("toggle"));
    expect(getByTestId("turn-load-kind").textContent).toBe("change · .86");
    expect(getByTestId("turn-load-tools").textContent).toContain("git_commit .88");
    expect(getByTestId("turn-load-guides").textContent).toBe("6 rendered · 9 left out of the prompt");
    expect(getByTestId("turn-load-floor").textContent).toBe("2 always offered: grep, read");
    expect(getByTestId("turn-load-engine").textContent).toBe("Bialy · 438 ms");
  });

  it("separates kept tools from the current decision without repeating their old scores", () => {
    const warm: TurnLoad = {
      ...load,
      tools: [
        { tool: "edit", source: "predicted", p: 0.71, carried: true },
        { tool: "web_search", source: "requested", need: "search the web", carried: true },
      ],
      boundary: { cache: "warm", idle_ms: 95_000, cold_after_ms: 360_000 },
    };
    const { getByTestId } = render(() => <TurnLoadCard row={{ ...toolsRow, load: warm }} layout="chat" />);
    const card = getByTestId("turn-load-card") as HTMLDetailsElement;
    card.open = true;
    card.dispatchEvent(new Event("toggle"));
    expect(getByTestId("turn-load-tools").textContent).toBe("None");
    expect(getByTestId("turn-load-kept-tools").textContent).toBe("edit, web_search");
    expect(getByTestId("turn-load-kept-tools").textContent).not.toContain(".71");
    expect(getByTestId("turn-load-boundary").textContent).toBe("Warm · idle 1m 35s · earlier tools kept");
  });

  it("keeps newly offered and carried tools in separate sections on a mixed turn", () => {
    const mixed: TurnLoad = {
      ...load,
      tools: [...load.tools, { tool: "page_open", source: "requested", carried: true }],
    };
    const { getByTestId } = render(() => <TurnLoadCard row={{ ...toolsRow, load: mixed }} layout="chat" />);
    const card = getByTestId("turn-load-card") as HTMLDetailsElement;
    card.open = true;
    card.dispatchEvent(new Event("toggle"));
    expect(getByTestId("turn-load-tools").textContent).toBe("git_commit .88, git_status .91");
    expect(getByTestId("turn-load-kept-tools").textContent).toBe("page_open");
  });

  it("names the fact that made a turn cold", () => {
    const cold: TurnLoad = { ...load, boundary: { cache: "cold", reason: "idle", idle_ms: 900_000, cold_after_ms: 360_000 } };
    const { getByTestId } = render(() => <TurnLoadCard row={{ ...toolsRow, load: cold }} layout="chat" />);
    const card = getByTestId("turn-load-card") as HTMLDetailsElement;
    card.open = true;
    card.dispatchEvent(new Event("toggle"));
    expect(getByTestId("turn-load-boundary").textContent).toBe("Cold · idle 15m 0s, past the 6m 0s the cache keeps · tools chosen again");
  });

});

it("shows the automatically preloaded skill separately from tools", () => {
  const withSkill: TurnLoad = { ...load, preloaded_skill: { name: "work-with-containers", score: 3.6 } };
  const { getByTestId } = render(() => <TurnLoadCard row={{ ...toolsRow, load: withSkill }} layout="chat" />);
  const card = getByTestId("turn-load-card") as HTMLDetailsElement;
  card.open = true;
  card.dispatchEvent(new Event("toggle"));
  expect(getByTestId("turn-load-preloaded-skill").textContent).toBe("work-with-containers · 3.60");
  expect(getByTestId("turn-load-tools").textContent).not.toContain("work-with-containers");
});

it("identifies a skill-only preload in the collapsed local AI row", () => {
  const row: TurnLoadRow = { ...toolsRow, load: { ...load, tools: [], preloaded_skill: { name: "verify-a-change", score: 3.5 } } };
  const part = turnLoadToolPart(row);
  expect(part.title).toBe("skill: verify-a-change");
  expect(part.completion?.operation).toBe("preloaded");
});
