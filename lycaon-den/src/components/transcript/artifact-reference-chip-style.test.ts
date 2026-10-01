import { readSourceText } from "../../test/stylesheet-source.ts";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const transcriptDir = dirname(fileURLToPath(import.meta.url));
const chatStyles = readSourceText(join(transcriptDir, "../../chat-utilities.css"), "utf8");

describe("artifact reference chip styling", () => {
  it("uses shared theme tokens instead of light-mode fallback colors", () => {
    const chip = chatStyles.match(/\.den-artifact-ref-chip\s*\{([\s\S]*?)\n\}/)?.[1] ?? "";
    const source = chatStyles.match(/\.den-artifact-ref-chip__source\s*\{([\s\S]*?)\n\}/)?.[1] ?? "";

    expect(chip).toContain("var(--den-chip-bg)");
    expect(chip).toContain("var(--den-chip-border)");
    expect(chip).toContain("var(--den-chip-fg)");
    expect(chip).not.toContain("var(--den-radius-sm,");
    expect(chatStyles).not.toMatch(/\.den-artifact-ref-chip:hover\s*\{/);
    expect(source).toContain("var(--den-text-muted)");
    expect(source).not.toMatch(/#(?:fff|f4f5f7|d0d4dc)/i);
    // Sits inside the chip button, so it carries no shape of its own.
    expect(source).not.toContain("border-radius");
    expect(source).not.toContain("background");
  });
});
