import { describe, expect, it } from "vitest";
import inventory from "./live-region-inventory.json";
import { LIVE_REGION_SELECTOR, classifyLiveRegion } from "./live-region-bridge.ts";
import { liveDeclarationCounts, scanLiveDeclarations } from "../../test/live-region-scan.ts";
import { denSourceRoot, loadSourceCorpus } from "../../test/source-corpus.ts";

const declarations = scanLiveDeclarations();
const POLITENESS = new Set(["polite", "assertive", "off"]);

function element(kind: "role" | "aria-live" | "output", value: string): Element {
  if (kind === "output") return document.createElement("output");
  const node = document.createElement("div");
  node.setAttribute(kind, value);
  return node;
}

describe("live-region inventory", () => {
  it("matches the reviewed inventory, so every new live region is checked against the bridge", () => {
    expect(
      liveDeclarationCounts(declarations),
      "Live regions changed. Confirm each new one is a plain DOM role/aria-live the bridge observes, then update live-region-inventory.json.",
    ).toEqual(inventory);
  });

  it("declares only regions the bridge recognizes", () => {
    const unrecognized = declarations.flatMap((declaration) => {
      const values = declaration.kind === "role"
        ? declaration.values.filter((value) => /\b(status|log|alert)\b/.test(value))
        : declaration.values.filter((value) => declaration.kind === "output" || POLITENESS.has(value));
      const misses = values.filter((value) => {
        const node = element(declaration.kind, value);
        return !node.matches(LIVE_REGION_SELECTOR)
          || classifyLiveRegion(node, node.getAttribute("aria-live")) === undefined;
      });
      return misses.map((value) => `${declaration.file}:${declaration.line} ${declaration.kind}=${value}`);
    });
    expect(unrecognized).toEqual([]);
  });

  it("uses only ARIA politeness values", () => {
    const invalid = declarations
      .filter((declaration) => declaration.kind === "aria-live")
      .flatMap((declaration) => declaration.values
        .filter((value) => !POLITENESS.has(value))
        .map((value) => `${declaration.file}:${declaration.line} aria-live=${value}`));
    expect(invalid).toEqual([]);
  });

  it("has no region the bridge cannot observe and no second path to the host", () => {
    const corpus = loadSourceCorpus(denSourceRoot, { extensions: [".ts", ".tsx"], excludeTests: true });
    const offenders = corpus.files.flatMap((file) => {
      const found: string[] = [];
      if (/\battachShadow\s*\(/.test(file.text)) found.push(`${file.rel}: shadow root`);
      if (/\bsrcdoc\b|\bsrcDoc\b/.test(file.text)) found.push(`${file.rel}: inline frame document`);
      if (file.text.includes("den_announce") && file.rel !== "platform/desktop/host-announcement.ts") {
        found.push(`${file.rel}: den_announce outside the host announcement module`);
      }
      return found;
    });
    expect(offenders).toEqual([]);
  });

  it("installs the bridge before the app renders", () => {
    const entry = loadSourceCorpus(denSourceRoot, { extensions: [".tsx"] }).files
      .find((file) => file.rel === "index.tsx")!.text;
    const install = entry.indexOf("setupLiveRegionBridge();");
    expect(install).toBeGreaterThan(-1);
    expect(install).toBeLessThan(entry.indexOf("render("));
  });
});
