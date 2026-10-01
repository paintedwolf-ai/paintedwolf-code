import { describe, expect, it, vi } from "vitest";
import { linkMenuItems } from "./link-menu-items.ts";
const confirm = vi.hoisted(() => vi.fn(async () => true));
vi.mock("../platform/desktop/external-link.ts", async (original) => ({ ...await original<typeof import("../platform/desktop/external-link.ts")>(), confirmAndOpenExternalLink: confirm }));
describe("web link destinations", () => {
  it("retains the external URL confirmation boundary", () => {
    const items = linkMenuItems("https://example.com/report");
    items[0]?.submenu?.[0]?.onSelect?.();
    expect(confirm).toHaveBeenCalledWith("https://example.com/report");
    expect(items[1]?.testId).toBe("link-menu-copy");
  });
  it("does not promote local paths or arbitrary schemes to browser actions", () => {
    for (const url of ["file:///repo/a.html", "javascript:alert(1)", "/repo/a.html"]) expect(linkMenuItems(url)).toEqual([]);
  });
});
