import { readSourceText } from "../test/stylesheet-source.ts";

import { join } from "node:path";
import { describe, expect, it } from "vitest";

const denSrc = join(import.meta.dirname, "..");

function read(rel: string): string {
  return readSourceText(join(denSrc, rel), "utf8");
}

describe("focus-visible + reduced-motion", () => {
  it("gives dock links, tab chips, and tool summaries focus-visible rings", () => {
    const chrome = read("global-components.css");
    const tools = read("tool-utilities.css");
    expect(chrome).toMatch(/\.den-shell-dock-link:focus-visible/);
    expect(chrome).toMatch(/\.tabs__chip:focus-visible/);
    expect(tools).toMatch(/\.den-tool-part > summary:focus-visible/);
  });

  it("zeros tab chrome fade and drawer motion under prefers-reduced-motion", () => {
    const globalCss = read("global.css");
    const drawer = read("drawer-utilities.css");
    expect(globalCss).toMatch(
      /@media \(prefers-reduced-motion:\s*reduce\)[\s\S]*--den-tabs-chrome-fade:\s*0ms/,
    );
    expect(drawer).toMatch(
      /@media \(prefers-reduced-motion:\s*reduce\)[\s\S]*\.den-context-drawer/,
    );
    // ComposerChromeSlot mounts and unmounts synchronously, so it has no transition to assert.
  });
});

describe("control outline affordance", () => {
  function denCheckboxBody(): string {
    const tw = read("tailwind.css");
    const start = tw.indexOf("@utility den-checkbox {");
    expect(start).toBeGreaterThan(-1);
    const rest = tw.slice(start);
    return rest.slice(0, rest.indexOf("\n}\n"));
  }

  it("draws the checkbox ring from --den-control-line, never the divider hairline", () => {
    const body = denCheckboxBody();
    expect(body).toMatch(/box-shadow:\s*0 0 0 1px var\(--den-control-line\) inset/);
    expect(body).not.toMatch(/var\(--den-line\)/);
  });

  it("lifts --den-control-line off the hairline in dark, where --den-line goes invisible", () => {
    // Keyed to the applied theme rather than the OS scheme.
    const globalCss = read("global.css");
    const dark = globalCss.slice(
      globalCss.indexOf(':root[data-den-appearance="dark"]'),
    );
    expect(dark).toMatch(/--den-control-line:\s*color-mix\([^;]*var\(--den-text\)/);
  });

  it("disables internal focus rings for embedded query inputs like scans", () => {
    const scans = read("styling/scans/stage-domain.css");
    expect(scans).toMatch(/\.den-scans-query input:focus-visible[\s\S]*box-shadow:\s*none/);
  });
});
