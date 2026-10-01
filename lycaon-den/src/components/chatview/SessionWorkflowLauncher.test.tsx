import { describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { readSourceText } from "../../test/stylesheet-source.ts";
import { join } from "node:path";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { SessionWorkflowLauncher } from "./SessionWorkflowLauncher.tsx";
import { sessionLauncherTiles } from "../../workflow/session-launcher-model.ts";
import type { WorkflowSummary } from "../../api/types.ts";

const catalog: WorkflowSummary[] = [
  { id: "plan", version: "1.0.0", name: "Plan", icon: "route", featured: true },
  {
    id: "recon-pack",
    version: "1.0.0",
    name: "Recon",
    icon: "radar",
    featured: true,
    requires_repo: true,
  },
];

describe("SessionWorkflowLauncher", () => {
  it("renders a tile per featured workflow plus a More tile", () => {
    render(() => (
      <SessionWorkflowLauncher
        tiles={sessionLauncherTiles(catalog, { hasRepo: true })}
        busy={false}
        onSelect={() => {}}
        onBrowseAll={() => {}}
      />
    ));
    const plan = screen.getByTestId("session-launcher-tile-plan");
    expect(plan).toBeTruthy();
    expect(plan.getAttribute("aria-label")).toBe("Plan");
    expect(screen.getByTestId("session-launcher-tile-recon-pack")).toBeTruthy();
    expect(screen.getByTestId("session-launcher-more").getAttribute("aria-label")).toBe(
      "More",
    );
  });

  it("selects a workflow when an enabled tile is clicked", () => {
    const onSelect = vi.fn();
    render(() => (
      <SessionWorkflowLauncher
        tiles={sessionLauncherTiles(catalog, { hasRepo: true })}
        busy={false}
        onSelect={onSelect}
        onBrowseAll={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("session-launcher-tile-plan"));
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: "plan" }));
  });

  it("marks the armed tile", () => {
    render(() => (
      <SessionWorkflowLauncher
        tiles={sessionLauncherTiles(catalog, { hasRepo: true })}
        busy={false}
        onSelect={() => {}}
        armedWorkflowId="plan"
        onBrowseAll={() => {}}
      />
    ));
    const plan = screen.getByTestId("session-launcher-tile-plan");
    expect(plan.getAttribute("aria-pressed")).toBe("true");
    expect(plan.classList.contains("den-session-launcher__tile--armed")).toBe(true);
  });

  it("disables a repo-oriented tile and shows the needs-repo affordance when no repo is attached", () => {
    const onSelect = vi.fn();
    render(() => (
      <SessionWorkflowLauncher
        tiles={sessionLauncherTiles(catalog, { hasRepo: false })}
        busy={false}
        onSelect={onSelect}
        onBrowseAll={() => {}}
      />
    ));
    const recon = screen.getByTestId("session-launcher-tile-recon-pack") as HTMLButtonElement;
    expect(recon.disabled).toBe(true);
    expect(screen.getByText("needs repo")).toBeTruthy();
    fireEvent.click(recon);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("opens the full catalog from the More tile", () => {
    const onBrowseAll = vi.fn();
    render(() => (
      <SessionWorkflowLauncher
        tiles={sessionLauncherTiles(catalog, { hasRepo: true })}
        busy={false}
        onSelect={() => {}}
        onBrowseAll={onBrowseAll}
      />
    ));
    fireEvent.click(screen.getByTestId("session-launcher-more"));
    expect(onBrowseAll).toHaveBeenCalled();
  });

  it("omits the More tile when onBrowseAll is not provided", () => {
    render(() => (
      <SessionWorkflowLauncher
        tiles={sessionLauncherTiles(catalog, { hasRepo: true })}
        busy={false}
        onSelect={() => {}}
        heading="Start a supporting workflow"
      />
    ));
    expect(screen.queryByTestId("session-launcher-more")).toBeNull();
    expect(screen.getByText("Start a supporting workflow")).toBeTruthy();
  });
});

describe("tile grid reflow", () => {
  it("keeps tiles on one row that can shrink to the glyph", () => {
    const css = readSourceText(
      join(import.meta.dirname, "../../chat-utilities.css"),
      "utf8",
    );
    const grid = css.match(/\.den-session-launcher__grid\s*\{([^}]*)\}/)?.[1];
    expect(grid, "launcher grid rule missing").toBeTruthy();
    expect(grid).toMatch(/display:\s*flex/);
    expect(grid).toMatch(/flex-wrap:\s*nowrap/);
    expect(css).not.toMatch(
      /\.den-session-launcher__grid\s*\{[^}]*grid-template-columns/,
    );
    // Grid layout is independent of tile count.
    expect(css).not.toMatch(/--session-launcher-columns/);
    const tsx = readFileSync(
      join(import.meta.dirname, "SessionWorkflowLauncher.tsx"),
      "utf8",
    );
    expect(tsx).not.toMatch(/--session-launcher-columns/);
    const tile = css.match(/\.den-session-launcher__tile\s*\{([^}]*)\}/)?.[1];
    expect(tile).toMatch(/flex:\s*1 1 0/);
    expect(tile).toMatch(/min-width:/);
    expect(tile).toMatch(/container-name:\s*den-session-launcher-tile/);
  });

  it("hides the label when a tile is down to the glyph", () => {
    const css = readSourceText(
      join(import.meta.dirname, "../../chat-utilities.css"),
      "utf8",
    );
    const iconOnly = css.match(
      /@container den-session-launcher-tile \(max-width:\s*[\d.]+rem\)\s*\{([\s\S]*?)\n\}/,
    )?.[1];
    expect(iconOnly, "icon-only tile tier missing").toBeTruthy();
    expect(iconOnly).toMatch(/\.den-session-launcher__tile-title/);
    expect(iconOnly).toMatch(/display:\s*none/);
  });

  it("condenses the tile frame at the narrowest chat column", () => {
    const css = readSourceText(
      join(import.meta.dirname, "../../chat-utilities.css"),
      "utf8",
    );
    const tier = css.match(
      /@container den-session-launcher \(max-width:\s*30rem\)\s*\{([\s\S]*?)\n\}/,
    )?.[1];
    expect(tier, "narrow launcher tier missing").toBeTruthy();
    expect(tier).not.toMatch(/grid-template-columns/);
    expect(tier).toMatch(/min-width:\s*calc\(20px \+ 14px\)/);
    // Rem thresholds scale both layouts with the text size.
    expect(tier).not.toMatch(/max-width:\s*\d+px/);
    expect(tier).toMatch(/\.den-session-launcher__tile-icon\s*\{/);
  });
});
