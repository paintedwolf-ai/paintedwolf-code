import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { FindingDataflow } from "./FindingDataflow.tsx";

describe("FindingDataflow", () => {
  it("shows nested calls and partial evidence without inventing a source", () => {
    render(() => <FindingDataflow flow={{
      sink: { location: { uri: "app.py", start_line: 7 }, callee: { location: { uri: "helper.py", start_line: 2 } } },
    }} />);
    expect(screen.queryByText("Source")).toBeNull();
    expect(screen.getByText("Sink")).toBeTruthy();
    expect(screen.getByText("app.py:7")).toBeTruthy();
    expect(screen.getByText("helper.py:2")).toBeTruthy();
  });
});
