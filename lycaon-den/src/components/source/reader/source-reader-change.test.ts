import { describe, expect, it } from "vitest";
import {
  readerComparesSides,
  readerContentSide,
  readerShowsChangeMarks,
  sourceChangeFromPresence,
  sourceReaderChange,
  wholeFileChange,
  wholeFileOpChange,
} from "./source-reader-change.ts";

const side = (availability: string) => ({ path: "a.ts", sha256: "", lines: 1, availability });

describe("sourceReaderChange", () => {
  it("reads presence from availability on each side", () => {
    expect(sourceReaderChange({ before: side("absent"), after: side("available") })).toBe("added");
    expect(sourceReaderChange({ before: side("available"), after: side("absent") })).toBe("deleted");
    expect(sourceReaderChange({ before: side("available"), after: side("available") })).toBe("changed");
    expect(sourceReaderChange({ before: side("absent"), after: side("absent") })).toBe("absent");
  });

  it("treats a present side without readable text as present", () => {
    expect(sourceReaderChange({ before: side("binary"), after: side("absent") })).toBe("deleted");
    expect(sourceReaderChange({ before: side("not_captured"), after: side("available") })).toBe("changed");
  });
});

describe("reader presentation", () => {
  it("marks removals but never paints an added file as insertions", () => {
    expect(readerShowsChangeMarks(sourceChangeFromPresence(false, true))).toBe(false);
    expect(readerShowsChangeMarks(sourceChangeFromPresence(true, false))).toBe(true);
    expect(readerShowsChangeMarks(sourceChangeFromPresence(true, true))).toBe(true);
  });

  it("numbers a deleted file by its last contents", () => {
    expect(readerContentSide("deleted")).toBe("before");
    expect(readerContentSide("added")).toBe("after");
    expect(readerContentSide("changed")).toBe("after");
  });

  it("marks only arrivals and removals where every listed file already changed", () => {
    expect(wholeFileChange("added")).toBe("added");
    expect(wholeFileChange("deleted")).toBe("deleted");
    expect(wholeFileChange("changed")).toBeUndefined();
    expect(wholeFileChange("absent")).toBeUndefined();
    expect(wholeFileOpChange("create")).toBe("added");
    expect(wholeFileOpChange("delete")).toBe("deleted");
    expect(wholeFileOpChange("write")).toBeUndefined();
    expect(wholeFileOpChange("rename")).toBeUndefined();
  });

  it("offers side-by-side and context folding only when both sides exist", () => {
    expect(readerComparesSides("changed")).toBe(true);
    expect(readerComparesSides("added")).toBe(false);
    expect(readerComparesSides("deleted")).toBe(false);
    expect(readerComparesSides("absent")).toBe(false);
  });
});
