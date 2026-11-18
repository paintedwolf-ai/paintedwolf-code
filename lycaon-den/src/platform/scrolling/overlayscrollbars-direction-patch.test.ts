// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { OverlayScrollbars } from "overlayscrollbars";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DEN_SCROLL_DIRECTION_ATTR } from "./themed-scrollbars.ts";

const denRoot = join(import.meta.dirname, "..", "..", "..");
const patchPath = "patches/overlayscrollbars@2.16.0.patch";

describe("OverlayScrollbars direction patch", () => {
  afterEach(() => document.body.replaceChildren());

  it("takes direction from the published attribute, at init and when it changes", async () => {
    const host = document.body.appendChild(document.createElement("div"));
    // A computed-style read would see this and answer left-to-right.
    host.style.direction = "ltr";
    host.setAttribute(DEN_SCROLL_DIRECTION_ATTR, "rtl");
    const scrollbars = OverlayScrollbars(host, {});
    try {
      expect(scrollbars.state().directionRTL).toBe(true);
      host.setAttribute(DEN_SCROLL_DIRECTION_ATTR, "ltr");
      await vi.waitFor(() => expect(scrollbars.state().directionRTL).toBe(false));
    } finally {
      scrollbars.destroy();
    }
  });

  it("leaves scroll-driven handle offsets to the host's measure phase", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");
    const lines = patch.split("\n");
    const removals = lines.filter((line) => line.startsWith("-") && !line.startsWith("---"));
    const additions = lines.filter((line) => line.startsWith("+") && !line.startsWith("+++"));

    // The library's scroll listener leaves the offset read to the host.
    expect(removals.filter((line) => /^-\s+A\(\);$/.test(line))).toHaveLength(4);
    expect(additions.some((line) => /\bA\(\);/.test(line))).toBe(false);
  });
});
