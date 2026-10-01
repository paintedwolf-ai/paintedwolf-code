import { describe, expect, it } from "vitest";
import { dataflowSteps } from "./scan-dataflow.ts";

describe("dataflowSteps", () => {
  it("orders source returns before sink calls and retains call depth", () => {
    const at = (start_line: number) => ({ uri: "flow.py", start_line });
    const steps = dataflowSteps({
      source: { location: at(3), intermediates: [at(2)], callee: { location: at(1) } },
      intermediates: [at(4)],
      sink: { location: at(5), intermediates: [at(6)], callee: { location: at(7) } },
    });
    expect(steps.map((s) => [s.kind, s.location.start_line, s.depth])).toEqual([
      ["Source", 1, 1], ["Propagation", 2, 1], ["Return", 3, 0],
      ["Propagation", 4, 0], ["Call", 5, 0], ["Propagation", 6, 1], ["Sink", 7, 1],
    ]);
  });
});
