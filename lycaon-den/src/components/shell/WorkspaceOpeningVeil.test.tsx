import { readFileSync } from "node:fs";
import { join } from "node:path";
import { render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { WorkspaceOpeningVeil } from "./WorkspaceOpeningVeil.tsx";

const stageStyles = readFileSync(join(import.meta.dirname, "../../styling/shell/stage-domain.css"), "utf8");

describe("WorkspaceOpeningVeil", () => {
  it.each([false, true])("covers the rail while project content prepares (mirrored: %s)", (mirrored) => {
    render(() => <>
      <style>{stageStyles}</style>
      <div classList={{ "den-shell--orientation-mirrored": mirrored }} style={{ "--den-nav-slot-width": "252px" }}>
        <WorkspaceOpeningVeil visible><p>Opening workspace…</p></WorkspaceOpeningVeil>
      </div>
    </>);
    const veil = screen.getByTestId("shell-workspace-veil");
    const style = getComputedStyle(veil);
    expect(style.position).toBe("absolute");
    expect(style.inset).toBe("0px");
    expect(style.left).not.toContain("--den-nav-slot-width");
    expect(style.right).not.toContain("--den-nav-slot-width");
    expect(veil.classList.contains("den-shell-workspace-veil--hidden")).toBe(false);
  });

  it("keeps one full-shell veil mounted through the reveal", () => {
    const [visible, setVisible] = createSignal(true);
    render(() => (
      <div class="den-shell" data-testid="shell">
        <aside data-testid="project-rail">rail</aside>
        <main data-testid="workspace-columns">columns</main>
        <WorkspaceOpeningVeil visible={visible()}>
          <p>Opening workspace…</p>
        </WorkspaceOpeningVeil>
      </div>
    ));

    const shell = screen.getByTestId("shell");
    const veil = screen.getByTestId("shell-workspace-veil");
    expect(veil.parentElement).toBe(shell);
    expect(veil.classList.contains("den-shell-workspace-veil--hidden")).toBe(false);
    expect(screen.getByTestId("project-rail")).toBeTruthy();
    expect(screen.getByTestId("workspace-columns")).toBeTruthy();

    setVisible(false);
    expect(screen.getByTestId("shell-workspace-veil")).toBe(veil);
    expect(veil.classList.contains("den-shell-workspace-veil--hidden")).toBe(true);
    expect(veil.getAttribute("aria-hidden")).toBe("true");
    expect(veil.inert).toBe(true);
  });
});
