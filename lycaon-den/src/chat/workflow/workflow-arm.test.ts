import { describe, expect, it } from "vitest";
import type { WorkflowSummary } from "../../api/types.ts";
import {
  armedComposerPlaceholder,
  armedWorkflowFromPickerRow,
  matchWorkflowSlashPrefix,
  resolveComposerWorkflowStart,
  synthesizeArmedSlashLine,
} from "./workflow-arm.ts";

const catalog: WorkflowSummary[] = [
  {
    id: "plan",
    version: "1.0.0",
    name: "Plan",
    trigger: "/plan",
    request: { cadence: "once", question: "What would you like to plan?" },
  },
  {
    id: "options",
    version: "1.0.0",
    name: "Options",
    trigger: "/options",
    presets: [
      {
        id: "quick",
        name: "Quick decision",
        trigger: "/options-quick",
      },
    ],
  },
];

describe("workflow-arm", () => {
  it("matches longest slash trigger and remainder", () => {
    const hit = matchWorkflowSlashPrefix("/options-quick do it", catalog);
    expect(hit?.suggestion.trigger).toBe("/options-quick");
    expect(hit?.remainder).toBe("do it");
    expect(matchWorkflowSlashPrefix("/plan", catalog)?.remainder).toBe("");
  });

  it("arms from a picker row using manifest or preset trigger", () => {
    const plan = armedWorkflowFromPickerRow(catalog[0]!);
    expect(plan).toEqual({
      workflow_id: "plan",
      workflow_version: "1.0.0",
      trigger: "/plan",
      label: "Plan",
      request: { cadence: "once", question: "What would you like to plan?" },
    });
    const light = armedWorkflowFromPickerRow({
      ...catalog[1]!,
      start_preset_id: "quick",
    });
    expect(light?.trigger).toBe("/options-quick");
    expect(light?.preset_id).toBe("quick");
    expect(light?.label).toBe("Quick decision");
  });

  it("bare slash arms; slash+ask passes; armed ask synthesizes", () => {
    expect(
      resolveComposerWorkflowStart({
        text: "/plan",
        catalog,
        armed: null,
      }),
    ).toEqual({
      kind: "arm",
      armed: {
        workflow_id: "plan",
        workflow_version: "1.0.0",
        trigger: "/plan",
        label: "Plan",
        request: { cadence: "once", question: "What would you like to plan?" },
      },
    });

    expect(
      resolveComposerWorkflowStart({
        text: "/plan make tictactoe",
        catalog,
        armed: null,
      }),
    ).toEqual({ kind: "pass", text: "/plan make tictactoe" });

    const armed = armedWorkflowFromPickerRow(catalog[0]!);
    expect(
      resolveComposerWorkflowStart({
        text: "make tictactoe",
        catalog,
        armed,
      }),
    ).toEqual({ kind: "synthesize", text: "/plan make tictactoe" });
    expect(
      resolveComposerWorkflowStart({ text: "", catalog, armed }),
    ).toEqual({ kind: "synthesize", text: "/plan" });
  });

  it("lets an explicit slash override a prior arm", () => {
    const armed = armedWorkflowFromPickerRow(catalog[0]!);
    expect(
      resolveComposerWorkflowStart({
        text: "/options choose auth",
        catalog,
        armed,
      }),
    ).toEqual({ kind: "pass", text: "/options choose auth" });
    expect(
      resolveComposerWorkflowStart({
        text: "/options",
        catalog,
        armed,
      }).kind,
    ).toBe("arm");
  });

  it("builds placeholder and slash line helpers", () => {
    const armed = armedWorkflowFromPickerRow(catalog[0]!)!;
    expect(armedComposerPlaceholder(armed)).toBe("What would you like to plan?");
    expect(synthesizeArmedSlashLine(armed, "  ship it  ")).toBe("/plan ship it");
  });
});
