import { readSourceText } from "../test/stylesheet-source.ts";

import { join } from "node:path";
import { describe, expect, it } from "vitest";

const denSrc = join(import.meta.dirname, "..");

function read(rel: string): string {
  return readSourceText(join(denSrc, rel), "utf8");
}

describe("spoken content", () => {
  it("keeps list markers on prose lists the element reset strips", () => {
    // WebKit also stops announcing a list once its markers are gone.
    const md = read("markdown.css");
    expect(read("element-reset.css")).toMatch(/ol,\s*ul,\s*menu\s*\{\s*list-style:\s*none/);
    expect(md).toMatch(/\.markdown-body ul\s*\{\s*list-style:\s*disc/);
    expect(md).toMatch(/\.markdown-body ol\s*\{\s*list-style:\s*decimal/);
  });

  it("keeps fenced code from trapping transcript wheel", () => {
    // Fences scroll in a sideways scrollport, whose viewport hands vertical gestures on.
    expect(read("chat/markdown/markdown-render.ts")).toMatch(
      /wrapInScrollportFrame\(pre[^)]*axis:\s*"x"/,
    );
    const sideways = read("platform/themed-scrollbars.css")
      .match(/\[data-den-scrollport="x"\] > \.den-scrollport__viewport\s*\{[^}]*\}/);
    expect(sideways?.[0]).toMatch(/overflow-x:\s*auto/);
    expect(sideways?.[0]).toMatch(/overflow-y:\s*hidden/);
    expect(sideways?.[0]).toMatch(/overscroll-behavior-y:\s*auto/);
  });
});
