import { describe, expect, it } from "vitest";
import type { Message, BlueprintMeta } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { sortTranscriptItemsByOrd } from "./transcript-item-order.ts";
import { createTranscriptDisplayProjector, sameTranscriptItem } from "./transcript-display-projection.ts";
import { blueprintCardViewFromMeta } from "../../../blueprint/blueprint-card-view.ts";

function planMessage(
  id: string,
  ord: number,
  status: BlueprintMeta["status"],
  seq = ord,
): Message {
  const meta: BlueprintMeta = {
    blueprint_path: "plan-1",
    revision: 1,
    revision_key: `rev-${status}`,
    status,
    phase: status === "approved" ? "approved" : "ready",
    phase_label: status === "approved" ? "Building…" : "Ready for review",
    blueprint_title: "Release plan",
    can_approve: status === "awaiting_approval",
    collapsed: status === "approved",
    show_actions: status === "awaiting_approval",
  };
  return {
    id,
    ord,
    seq,
    role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
    kind: "blueprint",
    content: `# Plan\n\nStep for ${status}`,
    visibility: "transcript",
    blueprint: meta,
    created_at: "t",
  };
}

describe("plan card invariant", () => {
  it("holds ord position across lifecycle patches", () => {
    const proposed = planMessage("m-plan", 13, "proposed");
    const awaiting = planMessage("m-plan", 13, "awaiting_approval", 14);
    const approved = planMessage("m-plan", 13, "approved", 15);
    const revised = planMessage("m-plan", 13, "revised", 16);

    const baseline = createTranscriptDisplayProjector()([proposed], undefined)[0]!;
    const baselineOrd = 13;
    expect(baseline.filter((i) => i.kind === "blueprint_card")).toHaveLength(1);
    expect(baseline[0]!.key).toBe("m-plan");

    for (const patch of [awaiting, approved, revised]) {
      const items = createTranscriptDisplayProjector()([patch], undefined)[0]!;
      const cards = items.filter((i) => i.kind === "blueprint_card");
      expect(cards).toHaveLength(1);
      expect(cards[0]!.key).toBe("m-plan");
      const sorted = sortTranscriptItemsByOrd(items, [patch]);
      const cardIndex = sorted.findIndex((i) => i.kind === "blueprint_card");
      expect(cardIndex).toBeGreaterThanOrEqual(0);
      const ordById = new Map([[patch.id, patch.ord ?? 0]]);
      const cardOrd = ordById.get("m-plan");
      expect(cardOrd).toBe(baselineOrd);
    }
  });

  it("patches content without changing key or ord", () => {
    const before = planMessage("m-plan", 5, "proposed");
    const after = planMessage("m-plan", 5, "awaiting_approval", 99);
    const beforeItems = messagesToTranscriptItems([before]);
    const afterItems = messagesToTranscriptItems([after]);
    expect(beforeItems[0]!.key).toBe(afterItems[0]!.key);
    expect(sameTranscriptItem(beforeItems[0]!, afterItems[0]!)).toBe(false);
    expect(blueprintCardViewFromMeta(after.blueprint!).phase).toBe("ready");
  });
});
