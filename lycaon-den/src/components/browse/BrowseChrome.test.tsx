import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { BrowseChrome } from "./BrowseChrome.tsx";

const denSrc = join(import.meta.dirname, "../..");

function read(rel: string): string {
  return readFileSync(join(denSrc, rel), "utf8");
}

describe("BrowseChrome", () => {
  it("uses the chat-style drag surface behind its controls", () => {
    expect(read("components/browse/BrowseChrome.tsx")).toMatch(
      /<ChromeDragSurface\s+class="den-browse-chrome__drag-surface"/,
    );
    const css = read("browse-stage-domain.css");
    expect(css).toMatch(
      /\.den-browse-chrome__drag-surface\[data-tauri-drag-region\][\s\S]*?-webkit-app-region:\s*drag/,
    );
    expect(css).toMatch(/\.den-browse-chrome__primary[\s\S]*?pointer-events:\s*none/);
    expect(css).toMatch(/\.den-browse-chrome__chips[\s\S]*?pointer-events:\s*none/);
  });

  it("renders primary row only when primary+overflow without chips", () => {
    render(() => (
      <BrowseChrome
        primary={<input data-testid="probe-primary" />}
        overflow={<button type="button" data-testid="probe-overflow">···</button>}
      />
    ));
    expect(screen.getByTestId("browse-chrome")).toBeTruthy();
    expect(screen.getByTestId("probe-primary")).toBeTruthy();
    expect(screen.getByTestId("probe-overflow")).toBeTruthy();
    expect(screen.queryByText("chip-a")).toBeNull();
  });

  it("renders both rows when all slots filled", () => {
    render(() => (
      <BrowseChrome
        class="probe-one probe-two"
        title={<span>Security</span>}
        primary={<span data-testid="probe-primary">run</span>}
        overflow={<span data-testid="probe-overflow">···</span>}
        chips={<span data-testid="probe-chip">chip-a</span>}
        meta={<span data-testid="probe-meta">12</span>}
      />
    ));
    expect(screen.getByText("Security")).toBeTruthy();
    const chrome = screen.getByTestId("browse-chrome");
    expect(chrome.hasAttribute("data-den-chrome")).toBe(true);
    expect(chrome.classList.contains("probe-one")).toBe(true);
    expect(chrome.classList.contains("probe-two")).toBe(true);
    expect(screen.getByTestId("probe-primary")).toBeTruthy();
    expect(screen.getByTestId("probe-overflow")).toBeTruthy();
    expect(screen.getByTestId("probe-chip")).toBeTruthy();
    expect(screen.getByTestId("probe-meta")).toBeTruthy();
  });

  it("renders null when no slots", () => {
    const { container } = render(() => <BrowseChrome />);
    expect(container.querySelector("[data-testid='browse-chrome']")).toBeNull();
  });
});
