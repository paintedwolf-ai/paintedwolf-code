import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { ApprovalFileChanges } from "./ApprovalFileChanges.tsx";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";

vi.mock("../../platform/navigation/open-files-surface.ts", () => ({ openFilesSurface: vi.fn() }));
afterEach(() => vi.clearAllMocks());

describe("approval file changes", () => {
  it("opens the exact proposal in Files without resolving the approval", () => {
    render(() => <ApprovalFileChanges projectId="project" checkpointId="approval-1" changes={[{
      path: "AGENTS.md", root_id: "root", operation: "write",
      before: "Old instructions\n", after: "Proposed instructions\n",
      before_bytes: 17, after_bytes: 22,
    }]} />);
    fireEvent.click(screen.getByRole("button", { name: "View diff · AGENTS.md" }));
    expect(openFilesSurface).toHaveBeenCalledWith(expect.objectContaining({
      kind: "file-change-preview", projectId: "project", rootId: "root", path: "AGENTS.md",
      previewId: "approval-1:root:file:AGENTS.md", before: "Old instructions\n", after: "Proposed instructions\n",
    }));
  });

  it("describes unavailable content without presenting it as a deletion", () => {
    render(() => <ApprovalFileChanges projectId="project" checkpointId="approval-2" changes={[{
      path: "data.bin", operation: "write", before: "previous text", after: "",
      before_bytes: 13, after_bytes: 20, before_sha256: "old-hash", after_sha256: "new-hash",
      preview_note: "This file has no text preview.",
    }]} />);
    fireEvent.click(screen.getByRole("button", { name: "View diff · data.bin" }));
    expect(openFilesSurface).toHaveBeenCalledWith(expect.objectContaining({
      before: "", after: "", detail: expect.stringContaining("This file has no text preview."),
    }));
    const request = vi.mocked(openFilesSurface).mock.calls[0]?.[0];
    expect(request?.kind === "file-change-preview" && request.detail).toContain("After SHA-256: new-hash");
  });
  it.each([
    ["create", null, ""],
    ["delete", "", null],
    ["write", "", ""],
  ])("carries explicit %s presence for an empty file", (operation, before, after) => {
    render(() => <ApprovalFileChanges projectId="project" checkpointId="approval-empty" changes={[{
      path: "empty.txt", operation: operation!, before: "", after: "", before_bytes: 0, after_bytes: 0,
    }]} />);
    fireEvent.click(screen.getByRole("button", { name: "View diff · empty.txt" }));
    expect(openFilesSurface).toHaveBeenCalledWith(expect.objectContaining({ before, after }));
  });

});
