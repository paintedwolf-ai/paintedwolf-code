import { describe, expect, it } from "vitest";
import type { SearchReplaceFilePreview } from "../api/types.ts";
import {
  countHunksByContext,
  initReplaceSelection,
  reconcileReplaceSelection,
  replaceSelectionCounts,
  selectionToApplyFiles,
  setHunksCheckedByContext,
  toggleReplaceFile,
  toggleReplaceHunk,
} from "./search-replace-selection.ts";

const preview: SearchReplaceFilePreview[] = [
  {
    root_id: "r1",
    path: "a.ts",
    sha256: "aa",
    hunks: [
      { line: 1, end_line: 1, before: "a", after: "A", context: "code" as const },
      { line: 2, end_line: 2, before: "b", after: "B", context: "code" as const },
    ],
  },
  {
    root_id: "r1",
    path: "b.ts",
    sha256: "bb",
    hunks: [{ line: 3, end_line: 3, before: "c", after: "C", context: "code" as const }],
  },
];

describe("search-replace-selection", () => {
  it("checks every file and hunk by default", () => {
    const sel = initReplaceSelection(preview);
    expect(replaceSelectionCounts(sel)).toEqual({ matches: 3, files: 2 });
    expect(selectionToApplyFiles(sel)).toHaveLength(2);
  });

  it("unchecking a file clears its hunks", () => {
    let sel = initReplaceSelection(preview);
    sel = toggleReplaceFile(sel, 0, false);
    expect(replaceSelectionCounts(sel)).toEqual({ matches: 1, files: 1 });
    expect(selectionToApplyFiles(sel)[0]?.path).toBe("b.ts");
  });

  it("unchecking the last hunk unchecks the file", () => {
    let sel = initReplaceSelection(preview);
    sel = toggleReplaceHunk(sel, 1, 0, false);
    expect(sel.files[1]?.checked).toBe(false);
    expect(replaceSelectionCounts(sel)).toEqual({ matches: 2, files: 1 });
  });

  it("truncated apply-all gate uses zero when nothing selected", () => {
    let sel = initReplaceSelection(preview);
    sel = toggleReplaceFile(sel, 0, false);
    sel = toggleReplaceFile(sel, 1, false);
    expect(replaceSelectionCounts(sel).matches).toBe(0);
    expect(selectionToApplyFiles(sel)).toEqual([]);
  });
});

describe("context-class selection", () => {
  const mixed: SearchReplaceFilePreview[] = [
    {
      root_id: "r1",
      path: "a.ts",
      sha256: "aa",
      hunks: [
        { line: 1, end_line: 1, before: "code", after: "CODE", context: "code" as const },
        {
          line: 2,
          end_line: 2,
          before: "// note",
          after: "// NOTE",
          context: "comment_or_string" as const,
        },
      ],
    },
    {
      root_id: "r1",
      path: "b.ts",
      sha256: "bb",
      hunks: [
        {
          line: 3,
          end_line: 4,
          before: "\"multi\nline\"",
          after: "\"MULTI\nline\"",
          context: "comment_or_string" as const,
        },
      ],
    },
  ];

  it("counts hunks by context", () => {
    expect(countHunksByContext(mixed, "comment_or_string")).toBe(2);
    expect(countHunksByContext(mixed, "code")).toBe(1);
  });

  it("unchecks a context class across files, keeping code checked", () => {
    const sel = initReplaceSelection(mixed);
    const next = setHunksCheckedByContext(
      sel,
      mixed,
      "comment_or_string",
      false,
    );
    expect(next.files[0]!.hunks).toEqual([true, false]);
    expect(next.files[0]!.checked).toBe(true);
    expect(next.files[1]!.hunks).toEqual([false]);
    expect(next.files[1]!.checked).toBe(false);
    expect(replaceSelectionCounts(next)).toEqual({ matches: 1, files: 1 });
  });
});

describe("reconcileReplaceSelection", () => {
  const makePreview = (
    path: string,
    hunks: Array<{ before: string; after: string; line?: number }>,
  ): SearchReplaceFilePreview => ({
    root_id: "root",
    path,
    sha256: "sha-" + path,
    hunks: hunks.map((h, i) => ({
      line: h.line ?? i + 1,
      end_line: h.line ?? i + 1,
      before: h.before,
      after: h.after,
      context: "code",
    })),
  });

  it("keeps unchecked hunks across a preview re-run", () => {
    const first = [makePreview("a.ts", [
      { before: "one", after: "ONE" },
      { before: "two", after: "TWO" },
    ])];
    const selection = {
      files: [{
        rootId: "root", path: "a.ts", sha256: "sha-a.ts",
        checked: true, collapsed: true, hunks: [true, false],
      }],
    };
    // Line numbers shift after an edit above; content anchors identity.
    const second = [makePreview("a.ts", [
      { before: "one", after: "ONE", line: 5 },
      { before: "two", after: "TWO", line: 6 },
      { before: "three", after: "THREE", line: 7 },
    ])];
    const next = reconcileReplaceSelection(selection, first, second);
    expect(next.files[0]?.hunks).toEqual([true, false, true]);
    expect(next.files[0]?.collapsed).toBe(true);
  });

  it("initializes fresh when there is no prior selection", () => {
    const files = [makePreview("a.ts", [{ before: "x", after: "y" }])];
    expect(reconcileReplaceSelection(null, [], files).files[0]?.hunks).toEqual([
      true,
    ]);
  });
});
