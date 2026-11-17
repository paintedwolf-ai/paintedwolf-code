import { readSourceText } from "../test/stylesheet-source.ts";

import { join } from "node:path";
import { describe, expect, it } from "vitest";

const denSrc = join(import.meta.dirname, "..");

function read(rel: string): string {
  return readSourceText(join(denSrc, rel), "utf8");
}

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
