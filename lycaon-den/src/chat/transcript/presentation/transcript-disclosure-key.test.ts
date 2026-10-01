import { describe, expect, it } from "vitest";
import {
  parseTranscriptDisclosureKey,
  transcriptDisclosureKey,
} from "./transcript-disclosure-key.ts";

describe("transcript disclosure keys", () => {
  it("gives every surface its own key for the same record", () => {
    const id = "a1:tc-1";
    const keys = Object.values(transcriptDisclosureKey).map((make) => make(id));
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("reads back every key a surface wrote", () => {
    for (const make of Object.values(transcriptDisclosureKey)) {
      const key = make("a1:tc-1");
      expect(parseTranscriptDisclosureKey(key)).toBe(key);
    }
  });

  it("drops values no surface wrote", () => {
    for (const value of [undefined, null, "", "a1:tc-1", "tool:", ":tc-1", "row-1", "unknown:tc-1"]) {
      expect(parseTranscriptDisclosureKey(value)).toBeUndefined();
    }
  });

  it("refuses a key without a subject", () => {
    expect(() => transcriptDisclosureKey.tool("  ")).toThrow();
  });
});
