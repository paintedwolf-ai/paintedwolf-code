// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { workerSummaryBlocks } from "./WorkerSummarySection.tsx";

describe("workerSummaryBlocks", () => {
  it("reads each labelled block", () => {
    const blocks = workerSummaryBlocks("Contribution: Added refresh.\nVerification: Ran `go test`.\nRemaining: Nothing reported.");
    expect([...blocks.entries()]).toEqual([
      ["Contribution", "Added refresh."], ["Verification", "Ran `go test`."], ["Remaining", "Nothing reported."],
    ]);
  });
});
