import { describe, expect, it } from "vitest";
import { at } from "../../test/at.ts";
import { filmstripStepsFromToolOutput } from "./filmstrip-steps.ts";

describe("filmstripStepsFromToolOutput", () => {
  it("aligns navigate + action results to zip frames", () => {
    const output = `[page#3]
${JSON.stringify({
  final_url: "http://127.0.0.1:9/",
  frames: [
    { index: 0, caption: "initial", evidence_handle: "page#1" },
    { index: 1, caption: "click #next", evidence_handle: "page#2" },
    { index: 2, caption: "click #finish", evidence_handle: "page#3" },
  ],
  action_results: [
    { ok: true, type: "click", selector: "#next" },
    { ok: true, type: "click", selector: "#finish" },
  ],
})}`;
    const steps = filmstripStepsFromToolOutput(output, 3, [
      "initial",
      "click #next",
      "click #finish",
    ]);
    expect(steps).toHaveLength(3);
    expect(steps[0]).toMatchObject({
      label: "initial",
      detail: "Navigate idle · http://127.0.0.1:9/",
      evidenceHandle: "page#1",
    });
    expect(steps[1]).toMatchObject({
      label: "click #next",
      detail: "click · #next",
      evidenceHandle: "page#2",
    });
    expect(at(steps, 2).detail).toBe("click · #finish");
  });

  it("falls back to zip captions without tool JSON", () => {
    const steps = filmstripStepsFromToolOutput(null, 2, ["initial", "click #x"]);
    expect(steps[0]?.label).toBe("initial");
    expect(steps[0]?.detail).toBe("Navigate idle");
    expect(steps[1]?.label).toBe("click #x");
    expect(steps[1]?.detail).toBe("After: click #x");
  });
});
