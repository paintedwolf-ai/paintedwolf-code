import { readSourceText } from "../../test/stylesheet-source.ts";
import { render, screen } from "@solidjs/testing-library";

import { join } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CONTEXT_NAV_CATALOG } from "../../../shared/app-state-types.ts";
import { stageLabelFor } from "../stage/stage-registry.tsx";
import { LayoutDockButton, LayoutDockTray } from "./LayoutDock.tsx";

const denSrc = join(import.meta.dirname, "../..");

vi.mock("../../shell/layout-store.ts", () => ({
  workspaceOrientationPref: () => "standard",
}));

const isTauriRuntime = vi.fn(() => true);

vi.mock("../../platform/runtime.ts", () => ({
  tauriPlatform: () => "macos" as const,
  detectedPlatform: () => "macos" as const,
  tauriDragRegionProps: () => ({}),
  isTauriRuntime: () => isTauriRuntime(),
}));

function renderTray(overrides: Partial<Parameters<typeof LayoutDockTray>[0]> = {}) {
  const onPlacement = vi.fn();
  const onStageWindowChange = vi.fn();
  const onToggleConversation = vi.fn();
  const result = render(() => (
    <LayoutDockTray
      stageId="files"
      placement="inline"
      splitLive={false}
      hasConversation
      onPlacement={onPlacement}
      stageInWindow={false}
      onStageWindowChange={onStageWindowChange}
      onToggleOrientation={vi.fn()}
      onSwapColumns={vi.fn()}
      onResetSplitSize={vi.fn()}
      conversationHidden={false}
      contextHidden={false}
      onToggleContext={() => {}}
      onToggleConversation={onToggleConversation}
      {...overrides}
    />
  ));
  return { ...result, onPlacement, onStageWindowChange, onToggleConversation };
}

describe("LayoutDockTray", () => {
  // isTauriRuntime gates the peer-window action and the widen hints.
  beforeEach(() => {
    isTauriRuntime.mockReturnValue(true);
  });

  it("hides and shows a live split's conversation", () => {
    const shown = renderTray({ placement: "split", splitLive: true });
    const action = screen.getByTestId("layout-toggle-conversation") as HTMLButtonElement;
    expect(action.disabled).toBe(false);
    expect(action.textContent).toContain("Hide conversation");
    action.click();
    expect(shown.onToggleConversation).toHaveBeenCalledOnce();
    shown.unmount();

    renderTray({ placement: "split", splitLive: true, conversationHidden: true });
    expect(screen.getByTestId("layout-toggle-conversation").textContent).toContain(
      "Show conversation",
    );
  });

  it("offers the conversation action only in a live split", () => {
    renderTray();
    expect(
      (screen.getByTestId("layout-toggle-conversation") as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("names the staged view and offers a peer-window button in Tauri", () => {
    const { onStageWindowChange } = renderTray();
    expect(screen.getByTestId("layout-tray").getAttribute("aria-label")).toBe(
      "Layout · files",
    );
    expect(screen.getByTestId("layout-tile-inline")).toBeTruthy();
    expect(screen.getByTestId("layout-tile-split")).toBeTruthy();
    expect(screen.getByTestId("layout-tray").textContent).toContain(
      "Show files beside this conversation.",
    );
    expect(screen.getByTestId("layout-tray-collapse-note").textContent).toBe(
      "Hide either pane when you need the room; restore it without losing your place.",
    );
    const win = screen.getByTestId("layout-stage-window") as HTMLButtonElement;
    expect(win.tagName).toBe("BUTTON");
    expect(win.textContent).toBe("Open Files in new window");
    expect(win.querySelector("svg.den-theme-icon")).toBeTruthy();
    win.click();
    expect(onStageWindowChange).toHaveBeenCalledWith(true);
  });

  it("closes the stage's peer windows once one is open", () => {
    const { onStageWindowChange } = renderTray({ stageInWindow: true });
    const win = screen.getByTestId("layout-stage-window");
    expect(win.textContent).toBe("Close Files window");
    win.click();
    expect(onStageWindowChange).toHaveBeenCalledWith(false);
  });

  it("hides the window button outside Tauri", () => {
    isTauriRuntime.mockReturnValue(false);
    renderTray();
    expect(screen.queryByTestId("layout-stage-window")).toBeNull();
  });

  it("says the window will grow when a placement claims chrome", () => {
    const { onPlacement } = renderTray({
      widensFor: (placement) => placement === "split",
    });
    const split = screen.getByTestId("layout-tile-split");
    expect(split.hasAttribute("aria-disabled")).toBe(false);
    expect(split.getAttribute("data-tip")).toBe(
      "Split view — widens the window to make room.",
    );
    expect(screen.getByTestId("layout-tile-inline").hasAttribute("data-tip")).toBe(
      false,
    );
    split.click();
    expect(onPlacement).toHaveBeenCalledWith("split");
  });

  it("says Single view will grow when that placement claims chrome", () => {
    renderTray({
      widensFor: (placement) => placement === "inline",
    });
    expect(screen.getByTestId("layout-tile-inline").getAttribute("data-tip")).toBe(
      "Single view — widens the window to make room.",
    );
    expect(screen.getByTestId("layout-tile-split").hasAttribute("data-tip")).toBe(
      false,
    );
  });

  it("promises no widen outside Tauri, where there is no window to grow", () => {
    isTauriRuntime.mockReturnValue(false);
    renderTray({ widensFor: () => true });
    const split = screen.getByTestId("layout-tile-split");
    expect(split.hasAttribute("aria-disabled")).toBe(false);
    expect(split.hasAttribute("data-tip")).toBe(false);
  });

  it("offers Split before the project has a conversation", () => {
    const { onPlacement } = renderTray({ hasConversation: false });
    const split = screen.getByTestId("layout-tile-split");
    expect(split.hasAttribute("aria-disabled")).toBe(false);
    expect(screen.getByTestId("layout-tray").textContent).toContain(
      "Split view opens a conversation beside files.",
    );
    expect(screen.queryByTestId("layout-tray-collapse-note")).toBeNull();
    split.click();
    expect(onPlacement).toHaveBeenCalledWith("split");
  });

  it("reports the chosen placement", () => {
    const { onPlacement } = renderTray();
    screen.getByTestId("layout-tile-inline").click();
    expect(onPlacement).toHaveBeenCalledWith("inline");
  });

  it("reports Split through the shared placement action when the host can fit it", () => {
    const { onPlacement } = renderTray();
    screen.getByTestId("layout-tile-split").click();
    expect(onPlacement).toHaveBeenCalledWith("split");
  });

  it("swaps only a live split independently of workspace mirroring", () => {
    const onSwapColumns = vi.fn();
    const onToggleOrientation = vi.fn();
    const view = renderTray({ splitLive: true, onSwapColumns, onToggleOrientation });
    screen.getByRole("button", { name: "Swap chat and context" }).click();
    expect(onSwapColumns).toHaveBeenCalledOnce();
    expect(onToggleOrientation).not.toHaveBeenCalled();
    view.unmount();
    renderTray({ splitLive: false });
    expect((screen.getByTestId("layout-swap-columns") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("layout-toggle-orientation") as HTMLButtonElement).disabled).toBe(false);
  });

  it("mirrors the workspace from one toggle that shows the current orientation", () => {
    const onToggleOrientation = vi.fn();
    renderTray({ onToggleOrientation });
    const mirror = screen.getByTestId("layout-toggle-orientation") as HTMLButtonElement;
    expect(mirror.disabled).toBe(false);
    expect(mirror.getAttribute("aria-pressed")).toBe("false");
    mirror.click();
    expect(onToggleOrientation).toHaveBeenCalledOnce();
  });

  it("keeps workspace orientation available on Home", () => {
    renderTray({ stageId: null, onStageWindowChange: undefined });
    expect(screen.getByTestId("layout-tray").getAttribute("aria-label")).toBe(
      "Layout",
    );
    expect(screen.queryByTestId("layout-tile-inline")).toBeNull();
    expect(screen.getByTestId("layout-toggle-orientation")).toBeTruthy();
  });

  it("marks the stored placement on its tile, not whether the split is live", () => {
    const live = renderTray({ splitLive: true, placement: "split" });
    expect(
      screen.getByTestId("layout-tile-split").getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen.getByTestId("layout-tile-inline").getAttribute("aria-pressed"),
    ).toBe("false");
    live.unmount();

    // Tile follows stored placement while the split is collapsed.
    const preferred = renderTray({ splitLive: false, placement: "split" });
    expect(
      screen.getByTestId("layout-tile-split").getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen.getByTestId("layout-tile-inline").getAttribute("aria-pressed"),
    ).toBe("false");
    preferred.unmount();

    renderTray({ splitLive: false, placement: "inline" });
    expect(
      screen.getByTestId("layout-tile-inline").getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen.getByTestId("layout-tile-split").getAttribute("aria-pressed"),
    ).toBe("false");
  });

  it("leaves dismissal to the shell — no listeners of its own", () => {
    const source = readSourceText(join(denSrc, "components/shell/LayoutDock.tsx"), "utf8");
    expect(source).not.toMatch(/addEventListener/);
  });

  it("every catalog stage has a label the tray can name", () => {
    for (const stageId of CONTEXT_NAV_CATALOG) {
      expect(stageLabelFor(stageId).length).toBeGreaterThan(0);
    }
  });
});

describe("LayoutDockButton", () => {
  it("reports its button for dock interaction boundaries", () => {
    const elementRef = vi.fn();
    render(() => (
      <LayoutDockButton
        open={false}
        splitLive={false}
        stageId={null}
        onToggle={vi.fn()}
        elementRef={elementRef}
      />
    ));

    const button = screen.getByTestId("layout-dock-btn");
    expect(elementRef).toHaveBeenCalledOnce();
    expect(elementRef.mock.calls[0]?.[0]).toBe(button);
  });

  it("is the Settings row's twin and stays available with no project", () => {
    const { unmount } = render(() => (
      <LayoutDockButton
        open={false}
        splitLive={false}
        stageId={null}
        onToggle={vi.fn()}
      />
    ));
    const home = screen.getByTestId("layout-dock-btn");
    expect(home.className).toContain("den-shell-dock-link");
    expect(home.hasAttribute("disabled")).toBe(false);
    expect(home.hasAttribute("data-tip")).toBe(false);
    unmount();

    const onToggle = vi.fn();
    render(() => (
      <LayoutDockButton
        open
        splitLive
        stageId="files"
        onToggle={onToggle}
      />
    ));
    const live = screen.getByTestId("layout-dock-btn");
    expect(live.getAttribute("aria-expanded")).toBe("true");
    expect(live.getAttribute("aria-label")).toBe("Layout · Files");
    expect(live.querySelector(".den-layout-glyph--split")).toBeTruthy();
    live.click();
    expect(onToggle).toHaveBeenCalled();
  });
});

describe("dock chrome CSS", () => {
  it("splits the dock row evenly between its two links", () => {
    const text = readSourceText(join(denSrc, "global-components.css"), "utf8");
    expect(text).toMatch(/\.den-shell-dock-link\s*\{[^}]*flex:\s*1 1 0/);
  });

  it("washes the rail into the dock above the trays", () => {
    const text = readSourceText(join(denSrc, "global-components.css"), "utf8");
    const wash = text.match(
      /\.den-shell-aside-main > \.den-shell-nav-dock::after\s*\{([^}]*)\}/,
    )?.[1];
    expect(wash, "dock wash missing").toBeTruthy();
    expect(wash).toMatch(/bottom:\s*100%/);
    // Rail wash uses --den-plane so it follows the chrome theme.
    expect(wash).toMatch(/linear-gradient\(to top, var\(--den-plane\), transparent\)/);
    expect(wash).toMatch(/pointer-events:\s*none/);
  });

  it("uses a two-up view chooser", () => {
    const text = readSourceText(join(denSrc, "global-components.css"), "utf8");
    const tiles = text.match(/\.den-layout-tray__tiles\s*\{([^}]*)\}/)?.[1];
    expect(tiles).toMatch(/grid-template-columns:\s*repeat\(2, 1fr\)/);
  });
});

describe("tab panel cap CSS", () => {
  it("scopes the cap to the split host and keeps it off a percentage", () => {
    const text = readSourceText(join(denSrc, "global-components.css"), "utf8");
    expect(text).toMatch(
      /\.den-shell-stage-host--split\s+\.den-split-col--chat\s+\.tabs__panel-clip\s*\{[^}]*max-height:\s*40dvh/,
    );
    expect(text).not.toMatch(/^\.tabs__panel-clip\s*\{[^}]*max-height:/m);
    // A percentage max-height would resolve against the clip, not the host.
    expect(text).not.toMatch(/\.tabs__panel-clip\s*\{[^}]*max-height:\s*\d+%/);
  });
});

describe("chips collapse by container", () => {
  it("keeps the icon-only collapse CSS-only", () => {
    const text = readSourceText(join(denSrc, "global-components.css"), "utf8");
    expect(text).toMatch(/@container den-tab-chips \(max-width: 28rem\)/);
    const rail = readSourceText(
      join(denSrc, "components/nav/ChatTabRail.tsx"),
      "utf8",
    );
    expect(rail).not.toMatch(/clientWidth|offsetWidth|innerWidth/);
  });
});
