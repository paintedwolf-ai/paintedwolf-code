import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { SECURITY_SCANNERS_SECTION_LABEL } from "../../settings/settings-nav-model.ts";
import { SettingsNavSidebar } from "./SettingsNavSidebar.tsx";

describe("SettingsNavSidebar", () => {
  it("lists only app-level settings sections", () => {
    render(() => (
      <SettingsNavSidebar section="providers" onSectionChange={vi.fn()} />
    ));

    const nav = screen.getByRole("navigation", { name: "Settings sections" });
    const labels = Array.from(
      nav.querySelectorAll(".den-shell-nav-sub-link-label"),
    ).map((b) => b.textContent);
    expect(labels).toEqual([
      "General",
      "AI providers",
      "Approvals",
      "MCP providers",
      SECURITY_SCANNERS_SECTION_LABEL,
      "Web research",
      "Extensions",
      "Cost",
      "Advanced",
    ]);
    expect(screen.queryByText("Editor")).toBeNull();
    expect(screen.queryByText("Keyboard")).toBeNull();
    expect(screen.queryByText("About")).toBeNull();
    expect(screen.queryByText("Models")).toBeNull();
    expect(screen.queryByText("Tests")).toBeNull();
    expect(screen.queryByText("Revoke approvals")).toBeNull();
  });

  it("renders one travelling marker and marks the selected row active", () => {
    render(() => (
      <SettingsNavSidebar section="approvals" onSectionChange={vi.fn()} />
    ));

    expect(document.querySelectorAll(".den-nav-marker")).toHaveLength(1);
    const active = document.querySelectorAll(".den-shell-nav-sub-link-active");
    expect(active).toHaveLength(1);
    expect(active[0]!.querySelector(".den-shell-nav-sub-link-label")?.textContent).toBe(
      "Approvals",
    );
  });
});
