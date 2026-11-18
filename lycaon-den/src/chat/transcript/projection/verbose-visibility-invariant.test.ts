import { describe, expect, it } from "vitest";
import type { CommandActivity, CommandProgramReceipt, Message, ToolResultUiVisibility } from "../../../api/types.ts";
import { clearCommandActivities, observeCommandActivity } from "../../tool/command-activity-live.ts";
import { activitySpanToolParts } from "../../tool/activity-span-model.ts";
import { PROGRAM_ACTIVITY_TOOLS, shouldOmitToolPartFromTranscript } from "../../tool/tool-transcript-visibility.ts";
import { buildChatTranscriptBlocks } from "../../workflow/workflow-spans.ts";
import { visibleRowContinuity, withdrawnRows } from "../presentation/row-withdrawal-watch.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
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
    expect(inFlight).toContain("instruct_worker");
    expect(inFlight).not.toContain("command");
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

function observeProgram(sessionId: string, tool: string, activities: CommandActivity[]): void {
  observeCommandActivity({
    session_id: sessionId,
    tool_call_id: `call-${tool}`,
    tool_name: tool,
    activity_id: "lease",
    kind: "running_tool",
    status: "active",
    started_at: "t0",
    progress: {
      phase: "executing",
      files_searched: 0,
      files_skipped: 0,
      matches: 0,
      command_program: { id: "program", state: "running", activities } as CommandProgramReceipt,
    },
  });
}

const admittedActivity: CommandActivity = {
  id: "execution",
  label: "Read files",
  subject: "README.md",
  group: "Reading files",
  state: "running",
  outcome: "Running",
  execution_ids: [],
  spans: [],
};

const scenarios = [
  { name: "unadmitted benign refusal", progress: false, visibility: "benign", quiet: [0, 0, 0, 0] },
  { name: "normal settlement without progress", progress: false, visibility: "normal", quiet: [0, 0, 0, 1] },
  { name: "admitted normal settlement", progress: true, visibility: "normal", quiet: [0, 1, 1, 1] },
  { name: "admitted settlement stamped benign", progress: true, visibility: "benign", quiet: [0, 1, 1, 1] },
] as const;

describe.each([...PROGRAM_ACTIVITY_TOOLS])("%s command program", (tool) => {
  it.each(scenarios)("preserves call identity through $name", ({ name, progress, visibility, quiet }) => {
    for (const verboseMode of [false, true]) {
      const sessionId = `session-${tool}-${name}-${verboseMode}`;
      const options = { sessionId, verboseMode };
      const assistant: Message = {
        id: "assistant-1", ord: 1, role: "assistant", origin: "model", authority: "none",
        trust_tier: "trusted", visibility: "transcript", content: "", created_at: "t0",
        tool_calls: [{ id: `call-${tool}`, name: tool, args: {} }],
      };
      const result = { ...settledResult(tool, visibility, "rejected"), ord: 2 };
      const project = createTranscriptDisplayProjector();
      const counts: number[] = [];
      let previous = new Map<string, string>();
      const frame = (messages: Message[]) => {
        const spans = buildChatTranscriptBlocks(messages, [], undefined, options);
        const items = project(messages, spans.map((span) => span.items), options).flat();
        const continuity = visibleRowContinuity(items);
        expect(withdrawnRows(previous, continuity, new Set([assistant.id]))).toEqual([]);
        previous = continuity;
        const keys = items.flatMap((item) => item.kind === "tool" ? [item.part.id] :
          item.kind === "activity_span" ? activitySpanToolParts(item.entries).map((part) => part.id) : []);
        expect(keys.every((key) => key === `${assistant.id}:call-${tool}`)).toBe(true);
        counts.push(keys.length);
      };
      try {
        observeProgram(sessionId, tool, []);
        frame([assistant]);
        if (progress) observeProgram(sessionId, tool, [admittedActivity]);
        const internal = { ...assistant, visibility: "internal" } as const;
        const privateRows = project([internal], undefined, options)[0]!;
        expect(privateRows.some((item) => item.kind === "tool" || item.kind === "activity_span")).toBe(false);
        frame([assistant]);
        clearCommandActivities(sessionId);
        frame([assistant]);
        frame([assistant, result]);
        expect(counts).toEqual(verboseMode ? [1, 1, 1, 1] : quiet);
      } finally {
        clearCommandActivities(sessionId);
      }
    }
  });
});
