import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { SourceWalkFile, SourceWalkResponse } from "../../api/types.ts";
import { stubFilesClient as stubClient } from "../../test/source-client-fixture.ts";
import { FilesEditor } from "../editor/FilesEditor.tsx";
import { PROJECT, loadedBuffer, editorProps, resetProjectFilesViewTest } from "../components/project-files-view-test-harness.ts";
import { setSidebarScope } from "./review-pane.ts";
import { isInScope, isDeletedInScope, resolveScope, scopeComparisonTarget, scopeDiffRevision } from "../tree/scope-resolution.ts";
import { buildReviewFileRows } from "./review-model.ts";
import { buildWalk } from "../walk/walk-model.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";

function file(path: string, op: "write" | "create" | "delete" = "write"): SourceWalkFile {
  return { file_id: "", root_id: "r1", path, changed_since_presented: false, unpresented_agent_effects: 0,
    tip: op === "delete" ? { state: "absent" } : { state: "content", sha256: `sha-${path}` }, head_match: "differs", effects: [],
    commit: { head: "commit-1", status: ".M", op, availability: op === "delete" ? "absent" : "available", history_truncated: false } };
}
function page(files: SourceWalkFile[], next?: string): SourceWalkResponse {
  return { baseline: "commit", files, commands: [], git_changes: [], turns: [], commit_available: true, next_cursor: next,
    commit_roots: [{ root_id: "r1", head: "commit-1", available: true }] };
}

beforeEach(() => { resetProjectFilesViewTest(); setSidebarScope(PROJECT, { kind: "commit" }); });

describe("complete Git review", () => {
  it("pages and marks paths without identities while leaving the chronological rail empty", async () => {
    const list = vi.fn().mockResolvedValueOnce(page([file("a.ts"), file("b.ts", "create")], "next"))
      .mockResolvedValueOnce(page([file("gone.ts", "delete")]));
    const result = await resolveScope(PROJECT, stubClient({ listProjectSourceWalk: list }), null);
    expect(result.files.map((row) => row.path)).toEqual(["a.ts", "b.ts", "gone.ts"]);
    expect(list.mock.calls[1]?.[1]).toMatchObject({ cursor: "next" });
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(true);
    expect(isDeletedInScope(PROJECT, "r1", "gone.ts")).toBe(true);
    expect(scopeComparisonTarget(PROJECT, "r1", "a.ts")).toEqual({ rootId: "r1", path: "a.ts", baseline: "commit", expectedHead: "commit-1" });
    expect(buildReviewFileRows(result.files).map((row) => row.op)).toEqual(["write", "create", "delete"]);
    expect(buildWalk("commit", result.files).steps).toHaveLength(0);
  });

  it("invalidates the comparison when HEAD changes without any working bytes changing", async () => {
    const row = file("a.ts");
    const api = stubClient({ listProjectSourceWalk: async () => page([row]) });
    await resolveScope(PROJECT, api, null);
    const before = scopeDiffRevision(PROJECT, "r1", "a.ts");
    row.commit = { ...row.commit!, head: "commit-2" };
    await resolveScope(PROJECT, api, null);
    expect(scopeDiffRevision(PROJECT, "r1", "a.ts")).not.toBe(before);
  });

  it("reports incomplete root coverage instead of treating unavailable folders as clean", async () => {
    const response = page([]);
    response.commit_roots!.push({ root_id: "r2", head: "", available: false, error: "Git is unavailable" });
    const result = await resolveScope(PROJECT, stubClient({ listProjectSourceWalk: async () => response }), null);
    expect(result.error).toContain("incomplete");
  });

  it("opens an unobserved deleted path as a committed-content comparison with restore", async () => {
    const row = file("gone.ts", "delete");
    const compare = vi.fn(async () => ({ in_range: true, location_changed: false,
      before: { state: "content", availability: "available", size_bytes: 19, content: "export const x = 1;\n" },
      after: { state: "absent", availability: "absent", size_bytes: 0 } }));
    const api = stubClient({ listProjectSourceWalk: async () => page([row]), readComparison: compare,
      listProjectSourceSymbols: async () => ({ sha256: "", symbols: [], truncated: false }) });
    await resolveScope(PROJECT, api, null);
    const buffer = { ...loadedBuffer({ path: "gone.ts", content: "" }), sourcePresent: false };
    const restore = vi.fn();
    render(() => <FilesEditor {...editorProps(buffer)} client={api} onRevertFile={restore} />);
    await waitFor(() => expect(screen.getByTestId("file-deletion-document").textContent).toContain("export const x = 1;"));
    fireEvent.click(screen.getByTestId("files-editor-restore-deleted"));
    expect(restore).toHaveBeenCalledOnce();
    expect(compare).toHaveBeenCalledWith(PROJECT, { rootId: "r1", path: "gone.ts", baseline: "commit", expectedHead: "commit-1" }, expect.anything());
  });

  it("reports an unavailable committed file and falls back to the deleted state", async () => {
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    try {
      const api = stubClient({ listProjectSourceWalk: async () => page([file("gone.ts", "delete")]),
        readComparison: async () => { throw new Error("Committed file unavailable"); },
        listProjectSourceSymbols: async () => ({ sha256: "", symbols: [], truncated: false }) });
      await resolveScope(PROJECT, api, null);
      const buffer = { ...loadedBuffer({ path: "gone.ts", content: "" }), sourcePresent: false };
      render(() => <FilesEditor {...editorProps(buffer)} client={api} onRevertFile={vi.fn()} />);
      expect(await screen.findByTestId("file-current-absent")).toBeTruthy();
      const rows = () => selectProjectNoticeGroups(notices.index()).find(group => group.projectId === PROJECT)?.notices ?? [];
      await waitFor(() => expect(rows()).toMatchObject([{ code: "files_committed_file_unavailable", message: "Committed file unavailable" }]));
      expect(screen.queryByTestId("files-editor-restore-deleted")).toBeNull();
    } finally {
      registerNoticePublisher(null);
    }
  });
});
