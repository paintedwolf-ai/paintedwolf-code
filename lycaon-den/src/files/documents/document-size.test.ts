import { describe, expect, it } from "vitest";
import { ChangeSet, Text } from "@codemirror/state";
import { DocumentByteBudget, EDITABLE_DOCUMENT_MAX_BYTES, encodedByteLength, utf8ByteLength } from "./document-size.ts";

function doc(text: string): Text { return Text.of(text.split("\n")); }

describe("document byte budget", () => {
  it("measures UTF-8 length like the encoder for every code point width", () => {
    for (const sample of ["", "plain", "é", "€", "🐺", "a🐺z\n", "\ud800 lone surrogate"]) {
      expect(utf8ByteLength(sample)).toBe(new TextEncoder().encode(sample).length);
    }
  });

  it("counts what the host writes: encoding, byte order mark, and serialized line endings", () => {
    const text = "a\nb\n";
    const units = text.length, utf8 = utf8ByteLength(text), lines = doc(text).lines;
    expect(encodedByteLength(units, utf8, lines, { encoding: "utf-8", eol: "lf" })).toBe(4);
    expect(encodedByteLength(units, utf8, lines, { encoding: "utf-8", eol: "crlf" })).toBe(6);
    expect(encodedByteLength(units, utf8, lines, { encoding: "utf-8-bom", eol: "lf" })).toBe(7);
    expect(encodedByteLength(units, utf8, lines, { encoding: "utf-16le", eol: "crlf" })).toBe(2 * 6 + 2);
  });

  it("admits without measuring while no encoding could reach the cap, then measures exactly", () => {
    const budget = new DocumentByteBudget(16);
    const small = doc("short");
    expect(budget.admits(doc(""), small, ChangeSet.of({ from: 0, insert: "short" }, 0), { encoding: "utf-8", eol: "lf" })).toBe(true);
    // Six wolves are 24 UTF-8 bytes but only 12 UTF-16 units: the ceiling is
    // not enough to decide, so the bytes are measured.
    const wolves = doc("🐺".repeat(6));
    expect(budget.admits(doc(""), wolves, ChangeSet.of({ from: 0, insert: "🐺".repeat(6) }, 0), { encoding: "utf-8", eol: "lf" })).toBe(false);
    const four = doc("🐺".repeat(4));
    expect(budget.admits(doc(""), four, ChangeSet.of({ from: 0, insert: "🐺".repeat(4) }, 0), { encoding: "utf-8", eol: "lf" })).toBe(true);
    // The next edit is followed by its own bytes rather than re-measured.
    const change = ChangeSet.of({ from: 8, insert: "🐺" }, four.length);
    expect(budget.admits(four, change.apply(four), change, { encoding: "utf-8", eol: "lf" })).toBe(false);
    const trim = ChangeSet.of({ from: 6, to: 8 }, four.length);
    expect(budget.admits(four, trim.apply(four), trim, { encoding: "utf-8", eol: "lf" })).toBe(true);
  });

  it("applies the host's cap by default", () => {
    const budget = new DocumentByteBudget();
    const limit = "a".repeat(EDITABLE_DOCUMENT_MAX_BYTES);
    expect(budget.admits(doc(""), doc(limit), ChangeSet.of({ from: 0, insert: limit }, 0), { encoding: "utf-8", eol: "lf" })).toBe(true);
    const over = doc(limit + "a");
    expect(budget.admits(doc(""), over, ChangeSet.of({ from: 0, insert: limit + "a" }, 0), { encoding: "utf-8", eol: "lf" })).toBe(false);
  });
});
