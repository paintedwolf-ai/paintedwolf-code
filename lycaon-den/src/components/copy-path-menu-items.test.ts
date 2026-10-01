import { describe, expect, it, vi } from "vitest";
import { copyPathMenuItems } from "./copy-path-menu-items.ts";

describe("copyPathMenuItems", () => {
  it("emits absolute then relative with the surface's test ids", () => {
    const items = copyPathMenuItems({
      copyAbsolute: vi.fn<() => void>(),
      copyRelative: vi.fn<() => void>(),
      absoluteTestId: "surface-copy-path",
      relativeTestId: "surface-copy-relative-path",
    });
    expect(items.map((i) => i.label)).toEqual([
      "Copy path",
      "Copy relative path",
    ]);
    expect(items.map((i) => i.testId)).toEqual([
      "surface-copy-path",
      "surface-copy-relative-path",
    ]);
  });

  it("drops the absolute item when the path is unresolvable", () => {
    const items = copyPathMenuItems({
      copyAbsolute: null,
      copyRelative: vi.fn<() => void>(),
      absoluteTestId: "surface-copy-path",
      relativeTestId: "surface-copy-relative-path",
    });
    expect(items.map((i) => i.label)).toEqual(["Copy relative path"]);
  });

  it("drops the relative item when the path has no containing root", () => {
    const items = copyPathMenuItems({
      copyAbsolute: vi.fn<() => void>(),
      absoluteTestId: "surface-copy-path",
      relativeTestId: "surface-copy-relative-path",
    });
    expect(items.map((i) => i.label)).toEqual(["Copy path"]);
  });

  it("routes each item to its own copier", () => {
    const copyAbsolute = vi.fn<() => void>();
    const copyRelative = vi.fn<() => void>();
    const items = copyPathMenuItems({
      copyAbsolute,
      copyRelative,
      absoluteTestId: "surface-copy-path",
      relativeTestId: "surface-copy-relative-path",
    });
    items[0]?.onSelect?.();
    items[1]?.onSelect?.();
    expect(copyAbsolute).toHaveBeenCalledTimes(1);
    expect(copyRelative).toHaveBeenCalledTimes(1);
  });

  it("appends file name and contents when those copiers are provided", () => {
    const copyFileName = vi.fn<() => void>();
    const copyContents = vi.fn<() => void>();
    const items = copyPathMenuItems({
      copyAbsolute: vi.fn<() => void>(),
      copyRelative: vi.fn<() => void>(),
      copyFileName,
      copyContents,
      absoluteTestId: "surface-copy-path",
      relativeTestId: "surface-copy-relative-path",
      fileNameTestId: "surface-copy-file-name",
      contentsTestId: "surface-copy-contents",
    });
    expect(items.map((i) => i.label)).toEqual([
      "Copy path",
      "Copy relative path",
      "Copy file name",
      "Copy contents",
    ]);
    expect(items.map((i) => i.testId)).toEqual([
      "surface-copy-path",
      "surface-copy-relative-path",
      "surface-copy-file-name",
      "surface-copy-contents",
    ]);
    items[2]?.onSelect?.();
    items[3]?.onSelect?.();
    expect(copyFileName).toHaveBeenCalledTimes(1);
    expect(copyContents).toHaveBeenCalledTimes(1);
  });
});
