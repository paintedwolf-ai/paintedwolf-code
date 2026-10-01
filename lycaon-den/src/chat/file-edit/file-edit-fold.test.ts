import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import {
  fileEditChange,
  fileEditFoldFingerprint,
  fileEditFoldIsNoop,
  fileEditLineStat,
  fileEditStepsFromParts,
  foldFileEdits,
  singleFileEditFold,
} from "./file-edit-fold.ts";
import type { FileEditStep } from "./file-edit-fold.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

function step(
  key: string,
  path: string,
  before: string | null,
  after: string,
  tool = "edit",
): FileEditStep {
  return { key, snapshot: fileEditPreviewFixture({ path, before, after }), tool };
}

const V1 = "a\nb\nc\n";
const V2 = "a\nB\nc\n";
const V3 = "a\nB\nc\nd\n";

describe("foldFileEdits", () => {
  it("composes a run of writes to one path into first-before → last-after", () => {
    const folds = foldFileEdits([
      step("t1", "pipeline.py", V1, V2),
      step("t2", "pipeline.py", V2, V3),
    ]);
    expect(folds).toHaveLength(1);
    expect(folds[0]!.net).toMatchObject({ path: "pipeline.py", before_sha256: fileEditPreviewFixture({ path: "pipeline.py", before: V1, after: V3 }).before_sha256,
      after_sha256: fileEditPreviewFixture({ path: "pipeline.py", before: V1, after: V3 }).after_sha256, added: null, removed: null });
    expect(folds[0]!.steps.map((s) => s.key)).toEqual(["t1", "t2"]);
  });

  it("keys the fold on the first write, so later writes do not move the card", () => {
    const one = foldFileEdits([step("t1", "a.ts", "x\n", "y\n")]);
    const two = foldFileEdits([
      step("t1", "a.ts", "x\n", "y\n"),
      step("t2", "a.ts", "y\n", "z\n"),
    ]);
    expect(two[0]!.key).toBe(one[0]!.key);
    expect(two[0]!.key).toBe("t1");
  });

  it("orders files by first touch and folds a path revisited later in the run", () => {
    const folds = foldFileEdits([
      step("t1", "pipeline.py", V1, V2),
      step("t2", "cli.py", "one\n", "two\n"),
      step("t3", "pipeline.py", V2, V3),
    ]);
    expect(folds.map((f) => f.path)).toEqual(["pipeline.py", "cli.py"]);
    expect(folds[0]!.steps).toHaveLength(2);
    expect(folds[0]!.net.after_sha256).toBe(fileEditPreviewFixture({path:"pipeline.py",after:V3}).after_sha256);
  });

  it("keeps a single write as a one-step fold", () => {
    const folds = foldFileEdits([step("t1", "a.ts", V1, V2)]);
    expect(folds[0]!.steps).toHaveLength(1);
    expect(folds[0]!.net).toEqual(fileEditPreviewFixture({ path: "a.ts", before: V1, after: V2 }));
  });

  it("returns nothing for no writes", () => {
    expect(foldFileEdits([])).toEqual([]);
  });

  it("carries a null pre-image through as a new file", () => {
    const folds = foldFileEdits([
      step("t1", "new.ts", null, "one\n"),
      step("t2", "new.ts", "one\n", "one\ntwo\n"),
    ]);
    expect(folds[0]!.net.created).toBe(true);
    expect(fileEditChange(folds[0]!.net)).toBe("added");
    expect(fileEditChange(folds[0]!.steps[1]!.snapshot)).toBe("changed");
  });
});

describe("fileEditChange", () => {
  it("names deletions and a file that never survived the range", () => {
    const deleted = fileEditPreviewFixture({ path: "gone.ts", before: "x\n", after: "", deleted: true });
    expect(fileEditChange(deleted)).toBe("deleted");
    const folds = foldFileEdits([
      step("t1", "tmp.ts", null, "x\n"),
      { key: "t2", snapshot: fileEditPreviewFixture({ path: "tmp.ts", before: "x\n", after: "", deleted: true }), tool: "delete" },
    ]);
    expect(fileEditChange(folds[0]!.net)).toBe("absent");
  });
});

describe("fileEditFoldIsNoop", () => {
  it("is true when a run put the file back where it found it", () => {
    const folds = foldFileEdits([
      step("t1", "a.ts", V1, V2),
      step("t2", "a.ts", V2, V1),
    ]);
    expect(fileEditFoldIsNoop(folds[0]!)).toBe(true);
    // Net-zero edits retain their individual writes.
    expect(folds[0]!.steps).toHaveLength(2);
  });

  it("preserves creation of an empty file", () => {
    expect(fileEditFoldIsNoop(singleFileEditFold("k", fileEditPreviewFixture({
      path: "a.ts",
      before: null,
      after: "",
    })))).toBe(false);
  });

  it("is false when the range changed the file", () => {
    expect(
      fileEditFoldIsNoop(foldFileEdits([step("t1", "a.ts", V1, V2)])[0]!),
    ).toBe(false);
  });
});

describe("fileEditLineStat", () => {
  it("counts added and removed lines", () => {
    expect(fileEditLineStat(fileEditPreviewFixture({ path: "a.ts", before: V1, after: V3 }))).toEqual({
      added: 2,
      removed: 1,
    });
  });

  it("is zero for identical content", () => {
    expect(fileEditLineStat(fileEditPreviewFixture({ path: "a.ts", before: V1, after: V1 }))).toEqual({
      added: 0,
      removed: 0,
    });
  });

  it("counts a new file as all additions", () => {
    expect(fileEditLineStat(fileEditPreviewFixture({ path: "a.ts", before: null, after: V1 }))).toEqual({
      added: 3,
      removed: 0,
    });
  });

  it("reports the range, not the sum of its writes", () => {
    // Reversing a write leaves the net diff empty.
    const folds = foldFileEdits([
      step("t1", "a.ts", V1, "a\nb\ndebug\nc\n"),
      step("t2", "a.ts", "a\nb\ndebug\nc\n", V1),
    ]);
    const perWrite = folds[0]!.steps.map((s) => fileEditLineStat(s.snapshot));
    expect(perWrite).toEqual([
      { added: 1, removed: 0 },
      { added: 0, removed: 1 },
    ]);
    expect(fileEditLineStat(folds[0]!.net)).toBeNull();
  });

});

describe("fileEditFoldFingerprint", () => {
  it("changes when a write lands on the same path", () => {
    const before = foldFileEdits([step("t1", "a.ts", V1, V2)])[0]!;
    const after = foldFileEdits([
      step("t1", "a.ts", V1, V2),
      step("t2", "a.ts", V2, V3),
    ])[0]!;
    expect(fileEditFoldFingerprint(after)).not.toBe(
      fileEditFoldFingerprint(before),
    );
  });

  it("is stable for the same writes", () => {
    const steps = [step("t1", "a.ts", V1, V2), step("t2", "a.ts", V2, V3)];
    expect(fileEditFoldFingerprint(foldFileEdits(steps)[0]!)).toBe(
      fileEditFoldFingerprint(foldFileEdits([...steps])[0]!),
    );
  });

  it("changes when a write's content changes under a stable key", () => {
    const a = foldFileEdits([step("t1", "a.ts", V1, V2)])[0]!;
    const b = foldFileEdits([step("t1", "a.ts", V1, V3)])[0]!;
    expect(fileEditFoldFingerprint(a)).not.toBe(fileEditFoldFingerprint(b));
  });
});

describe("fileEditStepsFromParts", () => {
  const part = (over: Partial<ToolPartView>): ToolPartView =>
    ({
      id: "p1",
      toolCallId: "c1",
      assistantMessageId: "m1",
      messageId: "m1",
      tool: "edit",
      kind: "write",
      status: "completed",
      args: {},
      output: "",
      error: null,
      fileEdit: null,
      ...over,
    }) as ToolPartView;

  it("takes one step per write, in call order, and skips calls that wrote nothing", () => {
    const steps = fileEditStepsFromParts([
      part({ id: "p1", fileEdit: fileEditPreviewFixture({ path: "a.ts", before: V1, after: V2 }) }),
      part({ id: "p2", tool: "read", kind: "read" }),
      part({ id: "p3", fileEdit: fileEditPreviewFixture({ path: "a.ts", before: V2, after: V3 }) }),
    ]);
    expect(steps.map((s) => s.key)).toEqual(["p1", "p3"]);
    expect(steps[0]!.tool).toBe("edit");
  });
});

describe("promoted file folds", () => {
  it("keeps equal relative paths in distinct roots separate and preserves deletion targets", () => {
    const folds = foldFileEdits([
      { key: "a", tool: "promote_overlay", snapshot: fileEditPreviewFixture({ root_id: "r1", path: "a.ts", after: "one" }) },
      { key: "b", tool: "promote_overlay", snapshot: fileEditPreviewFixture({ root_id: "r2", path: "a.ts", before: "two", after: "", deleted: true }) },
    ]);
    expect(folds).toHaveLength(2);
    expect(folds[0]?.net.root_id).toBe("r1");
    expect(folds[1]?.net).toMatchObject({ root_id: "r2", deleted: true });
  });
});
