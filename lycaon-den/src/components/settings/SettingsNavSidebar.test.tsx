import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { assertNamedControl } from "../../test/controls.ts";
import { SettingsNavSidebar } from "./SettingsNavSidebar.tsx";

describe("SettingsNavSidebar", () => {
  it("lists only app-level settings sections", () => {
    render(() => (
      <SettingsNavSidebar section="providers" onSectionChange={vi.fn()} />
    ));

    const nav = assertNamedControl(screen.getByTestId("settings-nav-sections"), "navigation");
    const ids = Array.from(
      nav.querySelectorAll("[data-testid^='settings-nav-']"),
    ).map((b) => b.getAttribute("data-testid")!.slice("settings-nav-".length));
    expect(ids).toEqual([
      "general",
      "providers",
      "approvals",
      "mcp",
      "scanners",
      "web-research",
      "extensions",
      "cost",
      "debug",
    ]);
  });

  it("renders one travelling marker and marks the selected row active", () => {
    render(() => (
      <SettingsNavSidebar section="approvals" onSectionChange={vi.fn()} />
    ));

    expect(document.querySelectorAll(".den-nav-marker")).toHaveLength(1);
    const active = document.querySelectorAll(".den-shell-nav-sub-link-active");
    expect(active).toHaveLength(1);
    expect(active[0]!.getAttribute("data-testid")).toBe(
      "settings-nav-approvals",
    );
  });
});
