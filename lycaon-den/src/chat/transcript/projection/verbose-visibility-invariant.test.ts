import { describe, expect, it } from "vitest";
import type { Message, ToolResultUiVisibility } from "../../../api/types.ts";
import { shouldOmitToolPartFromTranscript } from "../../tool/tool-part-model.ts";
import { TOOL_CHICKLET_TITLE_KEYS } from "../../tool/tool-presentation.generated.ts";
import {
  loadVisibilityFixture,
  withdrawnRowsDuringReplay,
} from "../testdata/visibility/assert-no-withdrawn-row.ts";

const CATALOG_TOOLS = Object.keys(TOOL_CHICKLET_TITLE_KEYS);

function settledResult(
  tool: string,
  uiVisibility: ToolResultUiVisibility,
  outcome: "completed" | "rejected" | "error",
): Message {
  return {
    id: `result-${tool}-${uiVisibility}-${outcome}`,
    role: "tool",
    origin: "tool",
    authority: "none",
    trust_tier: "untrusted",
    content: "settled",
    created_at: "t1",
    tool_result: {
      content: "settled",
      tool,
      tool_call_id: `call-${tool}`,
      assistant_message_id: "assistant-1",
      ui_visibility: uiVisibility,
      outcome,
    },
  } as Message;
}

describe("verbose visibility is monotone", () => {
  // Settling preserves visible tool rows.
  it("never renders a tool in flight that a settled result could hide", () => {
    const withdrawable: string[] = [];
    const inFlight: string[] = [];

    for (const tool of CATALOG_TOOLS) {
      const shownInFlight = !shouldOmitToolPartFromTranscript(undefined, tool, {
        verboseMode: false,
      });
      if (!shownInFlight) continue;
      inFlight.push(tool);

      for (const uiVisibility of ["benign", "normal"] as const) {
        for (const outcome of ["completed", "rejected", "error"] as const) {
          const hidden = shouldOmitToolPartFromTranscript(
            settledResult(tool, uiVisibility, outcome),
            tool,
            { verboseMode: false },
          );
          if (hidden) {
            withdrawable.push(`${tool} (ui_visibility=${uiVisibility}, outcome=${outcome})`);
          }
        }
      }
    }

    expect(
      withdrawable,
      "these tools render before their result and are then hidden by it: " +
        "either keep them visible for the whole lifecycle, or wait for the result",
    ).toEqual([]);
    // Verify the catalog includes both visibility modes.
    expect(inFlight).toContain("command");
    expect(inFlight).not.toContain("task");
  });

  // Replay covers rows that only appear between terminal states.
  it("withdraws no row while a refused worker-dispatch batch settles", () => {
    const withdrawn = withdrawnRowsDuringReplay(loadVisibilityFixture().frames);

    expect(
      withdrawn.map((row) => `${row.key} withdrawn at frame ${row.frame} by ${row.cause}`),
      "a row rendered from a model-authored call and taken back when the host refused it",
    ).toEqual([]);
  });
});

// Both visibility modes need replay coverage.
describe("recorded frames withdraw no step in either presentation", () => {
  for (const verboseMode of [false, true]) {
    it(`holds every rendered step with verboseMode=${verboseMode}`, () => {
      const withdrawn = withdrawnRowsDuringReplay(
        loadVisibilityFixture().frames,
        { verboseMode },
      );
      expect(withdrawn).toEqual([]);
    });
  }
});
