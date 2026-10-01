// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { SourceComparisonDigest } from "../../api/types.ts";
import { ReaderDocument, type ReaderSlot } from "../../components/source/reader/source-reader-document.ts";
import {
  PAGE_SECTION,
  diffsDocumentSlots,
  diffsTotal,
  sectionIsTitleOnly,
  sectionPlaceholder,
  sectionRows,
  type DiffsSectionState,
} from "./diffs-document.ts";
import { readOutcome } from "./diffs-reader.ts";

const measured = (rows: number, added = 1, removed = 1, folds = 0): SourceComparisonDigest => ({
  in_range: true, changes_rows: rows, changes_folds: folds,
  summary: { added, removed } as SourceComparisonDigest["summary"],
});

const row = (index: number, section: string): ReaderSlot => ({
  index, end: index + 1, text: `line ${index}\n`, kind: "equal",
  before_line: index + 1, after_line: index + 1, changed: [], section,
});

const section = (key: string, over: Partial<DiffsSectionState> = {}): DiffsSectionState =>
  ({ key, digest: measured(4), open: true, window: [], ...over });

describe("the page document", () => {
  it("leads with the page's own heading", () => {
    const slots = diffsDocumentSlots([section("a")]);
    expect(slots[0]?.section).toBe(PAGE_SECTION);
    expect(slots[0]?.header).toBe(true);
  });

  it("gives every file a header whether or not its rows are loaded", () => {
    const slots = diffsDocumentSlots([section("a"), section("b", { window: [row(0, "b")] })]);
    const headers = slots.filter((slot) => slot.header).map((slot) => slot.section);
    expect(headers).toEqual([PAGE_SECTION, "a", "b"]);
  });

  it("reserves an unread section from its measurement, not a guess", () => {
    const slots = diffsDocumentSlots([section("a", { digest: measured(37) })]);
    const body = slots.find((slot) => slot.section === "a" && !slot.header);
    expect(body?.pending).toBe(true);
    expect(body?.lines).toBe(37);
  });

  it("replaces the placeholder with the rows a section loaded", () => {
    const window = [row(0, "a"), row(1, "a")];
    const slots = diffsDocumentSlots([section("a", { window })]);
    const body = slots.filter((slot) => slot.section === "a" && !slot.header);
    expect(body).toEqual(window);
    expect(body.some((slot) => slot.pending)).toBe(false);
  });

  it("marks loaded rows as reloading when a section is in-flight reloading", () => {
    const window = [row(0, "a"), row(1, "a")];
    const slots = diffsDocumentSlots([section("a", { window, reloading: true })]);
    const body = slots.filter((slot) => slot.section === "a" && !slot.header);
    expect(body).toHaveLength(2);
    expect(body.every((slot) => slot.reloading)).toBe(true);
  });

  it("gives a collapsed section its header and nothing else", () => {
    const slots = diffsDocumentSlots([section("a", { open: false, window: [row(0, "a")] })]);
    expect(slots.filter((slot) => slot.section === "a")).toHaveLength(1);
  });

  it("gives an added or deleted file its name and no body", () => {
    for (const change of ["added", "deleted"] as const) {
      const slots = diffsDocumentSlots([section("a", { change, digest: measured(4000) })]);
      expect(slots.filter((slot) => slot.section === "a")).toHaveLength(1);
      expect(slots.filter((slot) => slot.section === "a")[0]?.header).toBe(true);
    }
  });

  it("keeps whole added or deleted files title-only even with revisions since details render in the header", () => {
    const slots = diffsDocumentSlots([section("a", { change: "added", revision: 1, digest: measured(40) })]);
    expect(slots.filter((slot) => slot.section === "a")).toHaveLength(1);
  });

  it("gives a revision with changed lines a body while keeping whole additions title-only", () => {
    const addedSlots = diffsDocumentSlots([section("a", { change: "added", revision: 0, digest: measured(4000) })]);
    expect(addedSlots.filter((slot) => slot.section === "a")).toHaveLength(1);

    const changedSlots = diffsDocumentSlots([section("a", { change: "changed", revision: 1, digest: measured(4000) })]);
    expect(changedSlots.filter((slot) => slot.section === "a")).toHaveLength(2);
    expect(changedSlots.find((slot) => slot.section === "a" && !slot.header)?.pending).toBe(true);
  });

  it("keeps a section's reserved body while its measurement holds", () => {
    const held = sectionPlaceholder("a", measured(37));
    expect(sectionPlaceholder("a", measured(37), held)).toBe(held);
    expect(sectionPlaceholder("a", measured(38), held)).not.toBe(held);
    // The page hands the held slot back, so its widget is never rebuilt.
    const slots = diffsDocumentSlots([section("a", { digest: measured(37), placeholder: held })]);
    expect(slots.find((slot) => slot.section === "a" && !slot.header)).toBe(held);
  });

  it("reads a chosen revision of a file whose net change is nothing", () => {
    const noop = { in_range: true, changes_rows: 0, summary: { added: 0, removed: 0 } } as SourceComparisonDigest;
    const slots = diffsDocumentSlots([section("a", { change: "changed", revision: 0, digest: noop })]);
    const body = slots.filter((slot) => slot.section === "a" && !slot.header);
    expect(body).toHaveLength(1);
    expect(body[0]?.pending).toBe(true);
    expect(sectionIsTitleOnly({ change: "changed", revision: null, digest: noop })).toBe(true);
  });

  it("gives a section that changes nothing no body to read", () => {
    const noop = { in_range: true, changes_rows: 0, summary: { added: 0, removed: 0 } } as SourceComparisonDigest;
    const slots = diffsDocumentSlots([section("a", { digest: noop })]);
    expect(slots.filter((slot) => slot.section === "a")).toHaveLength(1);
  });

  it("gives a binary section no body to read", () => {
    const binary = {
      in_range: true, changes_rows: 0,
      summary: {
        added: 0, removed: 0,
        before: { availability: "binary" },
        after: { availability: "binary" },
      },
    } as unknown as SourceComparisonDigest;
    const slots = diffsDocumentSlots([section("a", { digest: binary })]);
    expect(slots.filter((slot) => slot.section === "a")).toHaveLength(1);
  });

  it("keeps each file's rows in its own section", () => {
    const slots = diffsDocumentSlots([
      section("a", { window: [row(0, "a")] }),
      section("b", { window: [row(0, "b")] }),
    ]);
    const document = new ReaderDocument(slots);
    expect(document.rowAt("a", 0)).toBeDefined();
    expect(document.rowAt("b", 0)).toBeDefined();
    expect(document.rowAt("a", 0)?.from).not.toBe(document.rowAt("b", 0)?.from);
    expect(document.sections).toEqual([PAGE_SECTION, "a", "b"]);
  });

  it("gives each section a span of its own", () => {
    const slots = diffsDocumentSlots([
      section("a", { window: [row(0, "a"), row(1, "a")] }),
      section("b", { window: [row(0, "b")] }),
    ]);
    const document = new ReaderDocument(slots);
    const a = document.sectionSpan("a")!, b = document.sectionSpan("b")!;
    expect(a.to).toBeLessThanOrEqual(b.from);
  });
});

describe("what the page reports", () => {
  it("withholds the total until every file is measured", () => {
    expect(diffsTotal([section("a"), section("b", { digest: undefined })])).toBeNull();
  });

  it("adds the measured files up", () => {
    expect(diffsTotal([section("a", { digest: measured(4, 3, 2) }), section("b", { digest: measured(4, 1, 5) })]))
      .toEqual({ added: 4, removed: 7 });
  });

  it("reserves at least one row for an unmeasured section", () => {
    expect(sectionRows(undefined)).toBe(1);
  });
});

describe("a read the section gave up", () => {
  it("reports nothing when the page closed", () => {
    expect(readOutcome(new Error("boom"), true, 1, 1)).toBe("superseded");
  });

  it("reports nothing when another revision replaced the read", () => {
    expect(readOutcome(new Error("boom"), false, 1, 2)).toBe("superseded");
  });

  it("reports nothing when the section released its interest", () => {
    const cancelled = new DOMException("The presentation request was canceled.", "AbortError");
    expect(readOutcome(cancelled, false, 1, 1)).toBe("superseded");
  });

  it("reads again, quietly, when the comparison moved under the page", () => {
    // HEAD moving is what a baseline does; it is not a surface failure.
    for (const code of ["source_view_not_found", "source_view_revision_changed", "source_view_preparing"]) {
      expect(readOutcome(Object.assign(new Error("gone"), { code }), false, 1, 1)).toBe("moved");
    }
  });

  it("reports a failure the section is still waiting on", () => {
    expect(readOutcome(new Error("the host refused"), false, 1, 1)).toBe("failed");
    expect(readOutcome(Object.assign(new Error("nope"), { code: "source_view_unreadable" }), false, 1, 1)).toBe("failed");
    expect(readOutcome(Object.assign(new Error("File edit not found."), { code: "not_found" }), false, 1, 1)).toBe("failed");
  });
});
