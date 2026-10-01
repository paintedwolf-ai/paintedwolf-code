import { readSourceText } from "../../test/stylesheet-source.ts";

import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@solidjs/testing-library";
import { ProjectLoadingStage } from "./ProjectLoadingStage.tsx";

const componentsCss = readSourceText(
  join(import.meta.dirname, "..", "..", "global-components.css"),
  "utf8",
);

vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  usesCustomWindowChrome: () => true,
  tauriDragRegionProps: (options?: { deep?: boolean }) => ({
    "data-tauri-drag-region": options?.deep ? "deep" : "",
  }),
}));

afterEach(cleanup);

describe("ProjectLoadingStage tone", () => {
  it("waits quietly by default", () => {
    const { container } = render(() => <ProjectLoadingStage label="Lycaon" />);
    const stage = container.querySelector(".project-loading-stage");
    expect(stage?.getAttribute("aria-busy")).toBe("true");
    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(container.textContent).toContain("Opening workspace…");
  });

  it("reports a failed wait as an alert and stops claiming to be busy", () => {
    const { container } = render(() => (
      <ProjectLoadingStage
        label="Lycaon"
        hint="The editor workspace could not be opened."
        tone="error"
      />
    ));
    const stage = container.querySelector(".project-loading-stage");
    expect(stage?.getAttribute("aria-busy")).toBe("false");
    const hint = container.querySelector(".project-loading-stage__hint");
    expect(hint?.getAttribute("role")).toBe("alert");
    expect(hint?.classList.contains("project-loading-stage__hint--error")).toBe(true);
    expect(hint?.textContent).toBe("The editor workspace could not be opened.");
    expect(componentsCss).toMatch(/\.project-loading-stage__hint--error\s*\{[^}]*--den-danger-text/);
  });
});

describe("ProjectLoadingStage custom chrome", () => {
  it("drags the window from anywhere on the waiting surface", () => {
    const { container } = render(() => <ProjectLoadingStage label="Lycaon" />);

    const stage = container.querySelector(".project-loading-stage");
    expect(stage).not.toBeNull();
    // Bare regions drag on direct hits only; the centered copy covers the stage.
    expect(stage?.getAttribute("data-tauri-drag-region")).toBe("deep");
    expect(componentsCss).toMatch(
      /\.project-loading-stage\[data-tauri-drag-region\][\s\S]*?-webkit-app-region:\s*drag/,
    );
  });
});
