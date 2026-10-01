import { describe, expect, it } from "vitest";
import { canEditLoadedSource } from "./source-editor-model.ts";

describe("canEditLoadedSource", () => {
  it("allows edit when sha256 is present and buffer is editable text", () => {
    expect(
      canEditLoadedSource({
        kind: "text",
        overLimit: false,
        sha256: "abc",
        encoding: "utf-8",
        jobId: undefined,
      }),
    ).toBe(true);
    expect(
      canEditLoadedSource({
        kind: "text",
        overLimit: true,
        sha256: "abc",
        encoding: "utf-8",
      }),
    ).toBe(false);
    expect(
      canEditLoadedSource({
        kind: "text",
        overLimit: false,
        sha256: "abc",
        encoding: "utf-8",
        jobId: "j1",
      }),
    ).toBe(false);
    expect(
      canEditLoadedSource({
        kind: "image",
        overLimit: false,
        sha256: "abc",
        encoding: "utf-8",
      }),
    ).toBe(false);
    expect(
      canEditLoadedSource({
        kind: "text",
        overLimit: false,
        sha256: "",
        encoding: "utf-8",
      }),
    ).toBe(false);
    expect(
      canEditLoadedSource({
        kind: "text",
        overLimit: false,
        sha256: null,
        encoding: "utf-8",
      }),
    ).toBe(false);
    expect(
      canEditLoadedSource({
        kind: "text",
        overLimit: false,
        sha256: "abc",
        encoding: null,
      }),
    ).toBe(false);
  });
});
