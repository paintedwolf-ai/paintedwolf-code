import { describe, expect, it } from "vitest";
import {
  type InlineTextCapabilities,
  inlineTextWithinLimit,
  shouldAttachTextPaste,
  utf8ByteLength,
} from "./large-text-paste.ts";

const capabilities = {
  auto_attach_paste_bytes: 16,
  max_inline_text_bytes: 32,
} satisfies InlineTextCapabilities;

describe("large text paste policy", () => {
  it("measures UTF-8 bytes rather than JavaScript code units", () => {
    expect(utf8ByteLength("🐺")).toBe(4);
    expect(utf8ByteLength("é")).toBe(2);
    expect(utf8ByteLength("\ud800")).toBe(3);
  });

  it("keeps text below the threshold inline", () => {
    expect(shouldAttachTextPaste({
      current: "ask: ",
      selectionStart: 5,
      selectionEnd: 5,
      pasted: "small",
      capabilities,
    })).toBe(false);
  });

  it("attaches a paste at the advertised threshold", () => {
    expect(shouldAttachTextPaste({
      current: "",
      selectionStart: 0,
      selectionEnd: 0,
      pasted: "x".repeat(16),
      capabilities,
    })).toBe(true);
  });

  it("attaches a smaller paste when the resulting draft would exceed its cap", () => {
    expect(shouldAttachTextPaste({
      current: "x".repeat(30),
      selectionStart: 30,
      selectionEnd: 30,
      pasted: "abc",
      capabilities,
    })).toBe(true);
  });

  it("accounts for selected text that the paste replaces", () => {
    expect(shouldAttachTextPaste({
      current: "x".repeat(32),
      selectionStart: 8,
      selectionEnd: 24,
      pasted: "replacement",
      capabilities,
    })).toBe(false);
  });

  it("enforces the inline cap for non-paste input paths", () => {
    expect(inlineTextWithinLimit("x".repeat(32), capabilities)).toBe(true);
    expect(inlineTextWithinLimit("x".repeat(33), capabilities)).toBe(false);
  });
});
