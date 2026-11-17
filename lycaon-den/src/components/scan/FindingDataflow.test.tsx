import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { FindingDataflow } from "./FindingDataflow.tsx";

describe("FindingDataflow", () => {
  it("lists the call before the callee it reaches, and no source the evidence lacks", () => {
    render(() => <FindingDataflow flow={{
      sink: { location: { uri: "app.py", start_line: 7 }, callee: { location: { uri: "helper.py", start_line: 2 } } },
    }} />);
    const flow = screen.getByTestId("scans-dataflow");
    const steps = [...flow.querySelectorAll("dt")].map((kind, index) => [kind.textContent, flow.querySelectorAll("dd")[index]?.textContent]);
    expect(steps).toEqual([["Call", "app.py:7"], ["Sink", "helper.py:2"]]);
  });
});
