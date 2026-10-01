import { describe, expect, it, vi } from "vitest";
import {
  navigateProjectJumpBack,
  pushProjectJump,
  resetProjectJumpHistoryForTests,
} from "../../../files/components/project-files-jump-bridge.ts";
import { fileBufferKey } from "../../../files/components/project-files-model.ts";
import {
  openFinding,
  registerOpenFindingSink,
  resetOpenFindingForTests,
} from "../../../platform/navigation/open-finding.ts";

describe("knowing gutter jump history", () => {
  it("pushes the editor location before opening a finding", () => {
    resetProjectJumpHistoryForTests();
    resetOpenFindingForTests();
    const sink = vi.fn();
    registerOpenFindingSink(sink);

    pushProjectJump("proj", {
      bufferKey: fileBufferKey("root", "src/other.go"),
      rootId: "root",
      path: "src/other.go",
      line: 1,
    });
    pushProjectJump("proj", {
      bufferKey: fileBufferKey("root", "src/a.go"),
      rootId: "root",
      path: "src/a.go",
      line: 42,
    }, Date.now() + 1000);
    openFinding({ projectId: "proj", fingerprint: "fp1", scanId: "scan1" });

    expect(sink).toHaveBeenCalledWith({
      projectId: "proj",
      fingerprint: "fp1",
      scanId: "scan1",
    });
    const back = navigateProjectJumpBack("proj");
    expect(back).toEqual({
      bufferKey: fileBufferKey("root", "src/other.go"),
      rootId: "root",
      path: "src/other.go",
      line: 1,
    });
  });
});
