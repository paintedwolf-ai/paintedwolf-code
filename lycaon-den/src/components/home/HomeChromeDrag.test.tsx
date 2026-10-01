import { readFileSync } from "node:fs";
import { loaded } from "../../store/load-state.ts";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@solidjs/testing-library";
import { HomeView } from "./HomeView.tsx";

const homeAlignmentCss = readFileSync(
  join(import.meta.dirname, "home-alignment.css"),
  "utf8",
);

vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  usesCustomWindowChrome: () => true,
  tauriDragRegionProps: () => ({ "data-tauri-drag-region": "" }),
}));

afterEach(cleanup);

describe("HomeView custom chrome", () => {
  it("extends the draggable titlebar through the empty space above the idea input", () => {
    const { container } = render(() => (
      <HomeView
        summaries={[]}
        section="recents"
        registry={loaded(null)}
        onSubmitIdea={() => true}
        onOpenFolder={() => undefined}
        onCloneRepo={() => undefined}
        onOpenProject={() => undefined}
        onToggleStar={() => undefined}
        onRename={() => undefined}
        onAttachFolder={() => undefined}
        onPromote={() => undefined}
        onDelete={() => undefined}
      />
    ));

    const dragSurface = container.querySelector(".home-view__chrome-drag");
    expect(dragSurface).not.toBeNull();
    expect(dragSurface?.hasAttribute("data-tauri-drag-region")).toBe(true);
    expect(dragSurface?.nextElementSibling?.classList).toContain(
      "home-view__notices",
    );
    expect(homeAlignmentCss).toMatch(
      /\.home-view__chrome-drag\s*\{[\s\S]*?height:\s*var\(--home-view-top-inset\)/,
    );
    expect(homeAlignmentCss).toMatch(
      /\.home-view__chrome-drag\[data-tauri-drag-region\][\s\S]*?-webkit-app-region:\s*drag/,
    );
  });
});
