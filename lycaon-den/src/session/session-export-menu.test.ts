import { describe, expect, it } from "vitest";
import {
  messagesHaveExportableTranscript,
  sessionExportItems,
  sessionExportOverflowItems,
} from "./session-export-menu.ts";

describe("messagesHaveExportableTranscript", () => {
  it("is false for system-only / empty transcripts", () => {
    expect(messagesHaveExportableTranscript([])).toBe(false);
    expect(messagesHaveExportableTranscript([{ role: "system" }])).toBe(false);
  });

  it("is true when a user or assistant turn exists", () => {
    expect(
      messagesHaveExportableTranscript([
        { role: "system" },
        { role: "user" },
      ]),
    ).toBe(true);
  });
});

describe("sessionExportItems", () => {
  it("groups Markdown/JSON under a disabled Export submenu", () => {
    const calls: string[] = [];
    const items = sessionExportItems({
      disabled: true,
      onExport: (format) => calls.push(format),
    });
    expect(items.map((i) => i.label)).toEqual(["Export"]);
    expect(items[0]?.disabled).toBe(true);
    const formats = items[0]?.submenu ?? [];
    expect(formats.map((i) => i.label)).toEqual([
      "Export as Markdown",
      "Export as JSON",
    ]);
    formats[0]!.onSelect!();
    formats[1]!.onSelect!();
    expect(calls).toEqual(["md", "json"]);
  });
});

describe("sessionExportOverflowItems", () => {
  it("mirrors context-menu labels for the header overflow", () => {
    const calls: string[] = [];
    const items = sessionExportOverflowItems({
      onExport: (format) => calls.push(format),
    });
    expect(items.map((i) => i.testId)).toEqual([
      "session-export-md",
      "session-export-json",
    ]);
    expect(items.every((i) => i.disabled == null)).toBe(true);
    items[0]!.onSelect!();
    items[1]!.onSelect!();
    expect(calls).toEqual(["md", "json"]);
  });
});
