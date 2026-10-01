import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal, getOwner, onMount } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { TabBar } from "./TabBar.tsx";
import { nextOpen } from "../chatview/tabs.ts";
import {
  requestedFirstTimeTips,
  resetFirstTimeTipRequestsForTests,
} from "../../first-time-tips/first-time-tips-service.ts";
import { resetFirstTimeTipsPrefsForTests } from "../../settings/system/first-time-tips-prefs.ts";

vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  usesCustomWindowChrome: () => true,
}));
vi.mock("@tauri-apps/api/window", () => ({
  getCurrentWindow: () => ({ startDragging: vi.fn().mockResolvedValue(undefined) }),
}));

function dragRail(rail: HTMLElement, dy: number) {
  rail.dispatchEvent(
    new MouseEvent("pointerdown", { button: 0, clientX: 80, clientY: 10, bubbles: true }),
  );
  if (dy !== 0) {
    window.dispatchEvent(new MouseEvent("pointermove", { clientX: 80, clientY: 10 + dy }));
  }
  window.dispatchEvent(new MouseEvent("pointerup", {}));
}

function stubResizeObserver() {
  class WorkingResizeObserver {
    constructor(_callback: ResizeObserverCallback) {}
    observe() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", WorkingResizeObserver);
}

/** The mounted panel reports `panelHeight`; the clip reports it once open. */
function mockOpenPanelHeight(panelHeight: number): HTMLElement {
  const rect = (height: number): DOMRect => ({
    x: 0,
    y: 0,
    top: 0,
    right: 100,
    bottom: height,
    left: 0,
    width: 100,
    height,
    toJSON: () => ({}),
  });
  const clip = screen.getByTestId("tabs-clip") as HTMLElement;
  const panel = screen.getByTestId("tabs-panel") as HTMLElement;
  vi.spyOn(clip, "getBoundingClientRect").mockImplementation(() =>
    rect(clip.style.height === "0px" ? 0 : panelHeight),
  );
  vi.spyOn(panel, "getBoundingClientRect").mockImplementation(() => rect(panelHeight));
  return clip;
}

describe("tabs helpers", () => {
  it("nextOpen toggles the clicked tab and switches between tabs", () => {
    expect(nextOpen(null, "progress")).toBe("progress");
    expect(nextOpen("progress", "progress")).toBeNull();
    expect(nextOpen("progress", "git")).toBe("git");
  });
});

const tabs = [
  {
    id: "progress" as const,
    label: "Progress",
    panel: () => <div data-testid="p-progress">progress body</div>,
  },
  {
    id: "git" as const,
    label: "Git",
    panel: () => <div data-testid="p-git">git body</div>,
  },
];

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  resetFirstTimeTipRequestsForTests();
  resetFirstTimeTipsPrefsForTests(undefined, false);
});

describe("TabBar", () => {
  it("mounts panel components under a Solid owner", () => {
    let panelOwner: ReturnType<typeof getOwner> = null;
    const OwnedPanel = () => {
      panelOwner = getOwner();
      return <div>panel</div>;
    };
    render(() => (
      <TabBar
        open="progress"
        onOpenChange={() => {}}
        tabs={[{ id: "progress", label: "Progress", panel: OwnedPanel }]}
      />
    ));
    expect(panelOwner).toBeTruthy();
  });

  it("requests its first-time tip when rendered and opens the anchored panel on click", () => {
    resetFirstTimeTipsPrefsForTests({ enabled: true, dismissed: [] });
    render(() => (
      <TabBar
        tabs={[
          {
            id: "workflows",
            label: "Workflows",
            firstTimeTip: "workflows",
            panel: () => <div>Workflow panel</div>,
          },
        ]}
      />
    ));
    const workflows = screen.getByTestId("tab-workflows");
    expect(workflows.getAttribute("data-first-time-tip-anchor")).toBe("workflows");
    expect(requestedFirstTimeTips()).toEqual(["workflows"]);

    fireEvent.click(workflows);
    expect(screen.getByTestId("tabs-panel")).toBeTruthy();
  });

  it("renders chips collapsed with no panel until a tab is opened", () => {
    render(() => <TabBar tabs={tabs} />);
    const rail = screen.getByTestId("tabs").querySelector(".tabs__rail");
    expect(rail?.classList.contains("tabs__rail--compact")).toBe(true);
    expect(screen.getByTestId("tab-progress")).toBeTruthy();
    expect(screen.getByTestId("tab-git")).toBeTruthy();
    expect(screen.queryByTestId("tabs-panel")).toBeNull();
    expect(
      screen.getByTestId("tab-progress").getAttribute("aria-expanded"),
    ).toBe("false");
  });

  it("offers a tab label tooltip only for an icon chip whose label can disappear", () => {
    render(() => (
      <TabBar
        tabs={[
          { id: "progress", label: "Progress", panel: () => <div /> },
          {
            id: "git",
            label: "Git",
            icon: <span aria-hidden="true">G</span>,
            panel: () => <div />,
          },
        ]}
      />
    ));

    expect(screen.getByTestId("tab-progress").hasAttribute("data-tip")).toBe(false);
    expect(screen.getByTestId("tab-git").getAttribute("data-tip")).toBe("Git");
    expect(screen.getByTestId("tab-git").getAttribute("data-tip-when-clipped")).toBe(
      ".tabs__chip-label",
    );
  });

  it("uses compact rail labels until a tab opens, then shows full chrome", () => {
    vi.useFakeTimers();
    render(() => <TabBar tabs={tabs} />);
    const rail = () =>
      screen.getByTestId("tabs").querySelector(".tabs__rail") as HTMLElement;
    expect(rail().classList.contains("tabs__rail--compact")).toBe(true);
    fireEvent.click(screen.getByTestId("tab-git"));
    expect(rail().classList.contains("tabs__rail--compact")).toBe(false);
    fireEvent.click(screen.getByTestId("tab-git"));
    vi.advanceTimersByTime(200);
    expect(rail().classList.contains("tabs__rail--compact")).toBe(true);
  });

  it("renders leading and trailing chrome without tab chips", () => {
    render(() => (
      <TabBar
        tabs={[]}
        leading={<button type="button" data-testid="expand-btn" />}
        trailing={<button type="button" data-testid="trailing-btn" />}
      />
    ));
    expect(screen.getByTestId("tabs")).toBeTruthy();
    expect(screen.getByTestId("expand-btn")).toBeTruthy();
    expect(screen.getByTestId("trailing-btn")).toBeTruthy();
    expect(screen.queryByTestId("tabs-panel")).toBeNull();
  });

  it("keeps sidebar expand separate from the chip rail", () => {
    render(() => (
      <TabBar
        tabs={tabs}
        leading={<button type="button" data-testid="expand-btn" />}
      />
    ));
    expect(
      screen.getByTestId("tabs").querySelector(".tabs__rail-leading"),
    ).toBeTruthy();
    fireEvent.click(screen.getByTestId("tab-progress"));
    expect(
      screen.getByTestId("tabs").querySelector(".tabs__rail-leading"),
    ).toBeTruthy();
    const progress = screen.getByTestId("tab-progress");
    expect(screen.getByTestId("expand-btn")).toBeTruthy();
    expect(progress.classList.contains("tabs__chip--shown")).toBe(true);
    expect(
      screen.getByTestId("tabs-panel").closest(".tabs__strip"),
    ).toBeTruthy();
    expect(
      screen.getByTestId("tabs-panel").closest(".tabs__rail-leading"),
    ).toBeNull();
  });

  it("keeps trailing controls separate and the fade on the overlay wash", () => {
    render(() => (
      <TabBar
        tabs={tabs}
        trailing={<button type="button" data-testid="trailing-btn" />}
      />
    ));
    expect(
      screen.getByTestId("tabs").querySelector(".tabs__rail-trailing"),
    ).toBeTruthy();
    expect(screen.getByTestId("trailing-btn")).toBeTruthy();
    expect(
      screen.getByTestId("trailing-btn").closest(".tabs__rail-trailing"),
    ).toBeTruthy();
    fireEvent.click(screen.getByTestId("tab-git"));
    const fade = screen.getByTestId("tabs").querySelector(".tabs__panel-fade");
    expect(fade?.parentElement?.classList.contains("tabs__panel-wash")).toBe(true);
    expect(fade?.closest(".tabs__panel-shell")).toBeNull();
  });

  it("measures the first open after two layout frames", () => {
    const frames: FrameRequestCallback[] = [];
    vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
      frames.push(cb);
      return frames.length;
    });
    render(() => <TabBar tabs={tabs} />);
    fireEvent.click(screen.getByTestId("tab-git"));
    const clip = screen.getByTestId("tabs-clip") as HTMLElement;
    expect(clip.style.height).toBe("0px");
    expect(frames.length).toBe(1);
    frames.shift()!(0);
    expect(clip.style.height).toBe("0px");
    expect(frames.length).toBe(1);
    frames.shift()!(0);
    expect(clip.style.height === "auto" || clip.style.height.endsWith("px")).toBe(
      true,
    );
    vi.restoreAllMocks();
  });

  it("clicking a tab slides its panel down", () => {
    render(() => <TabBar tabs={tabs} />);
    fireEvent.click(screen.getByTestId("tab-git"));
    const panel = screen.getByTestId("tabs-panel");
    expect(panel.dataset.tab).toBe("git");
    expect(
      screen.getByTestId("tab-git").getAttribute("aria-expanded"),
    ).toBe("true");
  });

  it("clicking the open tab again slides the panel back up", () => {
    vi.useFakeTimers();
    render(() => <TabBar tabs={tabs} />);
    fireEvent.click(screen.getByTestId("tab-progress"));
    fireEvent.click(screen.getByTestId("tab-progress"));
    // Selection clears before the closing slide.
    expect(
      screen.getByTestId("tab-progress").getAttribute("aria-expanded"),
    ).toBe("false");
    expect(
      screen.getByTestId("tabs").querySelector(".tabs__rail")?.classList.contains(
        "tabs__rail--compact",
      ),
    ).toBe(false);
    vi.advanceTimersByTime(200);
    expect(screen.queryByTestId("tabs-panel")).toBeNull();
  });

  it("clicking a different tab swaps the panel after the slide", () => {
    vi.useFakeTimers();
    render(() => <TabBar tabs={tabs} />);
    fireEvent.click(screen.getByTestId("tab-progress"));
    expect(screen.getByTestId("tabs-panel").dataset.tab).toBe("progress");
    fireEvent.click(screen.getByTestId("tab-git"));
    // Selection updates before content swaps.
    expect(
      screen.getByTestId("tab-progress").getAttribute("aria-expanded"),
    ).toBe("false");
    expect(
      screen.getByTestId("tab-git").getAttribute("aria-expanded"),
    ).toBe("true");
    vi.runAllTimers();
    expect(screen.getByTestId("tabs-panel").dataset.tab).toBe("git");
  });

  it("is controllable via open / onOpenChange", () => {
    const onOpenChange = vi.fn();
    render(() => (
      <TabBar tabs={tabs} open="git" onOpenChange={onOpenChange} />
    ));
    expect(screen.getByTestId("tabs-panel").dataset.tab).toBe("git");
    fireEvent.click(screen.getByTestId("tab-git"));
    expect(onOpenChange).toHaveBeenCalledWith(null);
  });

  it("opens an empty tab's panel fully", () => {
    render(() => (
      <TabBar
        tabs={[
          {
            id: "progress",
            label: "Progress",
            empty: true,
            panel: () => <div data-testid="p-progress">nothing yet</div>,
          },
          { id: "git", label: "Git", panel: () => <div data-testid="p-git" /> },
        ]}
      />
    ));
    const chip = screen.getByTestId("tab-progress");
    expect(chip.hasAttribute("disabled")).toBe(false);
    expect(chip.classList.contains("tabs__chip--empty")).toBe(true);
    fireEvent.click(chip);
    expect(chip.classList.contains("tabs__chip--empty")).toBe(false);
    expect(chip.classList.contains("tabs__chip--shown")).toBe(true);
    const panel = screen.getByTestId("tabs-panel");
    expect(panel.dataset.tab).toBe("progress");
    expect(panel.textContent).toContain("nothing yet");
  });

  it("closes the open panel by clicking the active tab again", () => {
    vi.useFakeTimers();
    render(() => <TabBar tabs={tabs} />);
    fireEvent.click(screen.getByTestId("tab-progress"));
    expect(screen.getByTestId("tabs-panel")).toBeTruthy();
    fireEvent.click(screen.getByTestId("tab-progress"));
    expect(
      screen.getByTestId("tab-progress").getAttribute("aria-expanded"),
    ).toBe("false");
    vi.advanceTimersByTime(200);
    expect(screen.queryByTestId("tabs-panel")).toBeNull();
  });

  it("keeps an open tab open when it empties", () => {
    vi.useFakeTimers();
    const [empty, setEmpty] = createSignal(false);
    render(() => (
      <TabBar
        open="git"
        onOpenChange={() => {}}
        tabs={[
          { id: "progress", label: "Progress", panel: () => <div /> },
          {
            id: "git",
            label: "Git",
            empty: empty(),
            panel: () => <div data-testid="p-git">git body</div>,
          },
        ]}
      />
    ));
    const chip = () => screen.getByTestId("tab-git");
    expect(screen.getByTestId("tabs-panel").dataset.tab).toBe("git");
    expect(chip().classList.contains("tabs__chip--shown")).toBe(true);

    setEmpty(true);
    expect(chip().classList.contains("tabs__chip--shown")).toBe(true);
    expect(chip().classList.contains("tabs__chip--empty")).toBe(false);
    vi.runAllTimers();
    expect(screen.getByTestId("tabs-panel").dataset.tab).toBe("git");

    setEmpty(false);
    vi.runAllTimers();
    expect(screen.getByTestId("tabs-panel").dataset.tab).toBe("git");
  });

  it("keeps the open panel mounted when tab specs are replaced", () => {
    let mounts = 0;
    const GitPanel = () => {
      onMount(() => {
        mounts += 1;
      });
      return <div data-testid="p-git">git body</div>;
    };
    const [badge, setBadge] = createSignal("7");
    render(() => (
      <TabBar
        open="git"
        onOpenChange={() => {}}
        tabs={[
          {
            id: "git",
            label: "Git",
            badge: badge(),
            empty: badge() === "0",
            panel: () => <GitPanel />,
          },
        ]}
      />
    ));
    expect(screen.getByTestId("p-git")).toBeTruthy();
    expect(mounts).toBe(1);
    setBadge("8");
    setBadge("0");
    setBadge("8");
    expect(screen.getByTestId("p-git")).toBeTruthy();
    expect(mounts).toBe(1);
  });

  it("keeps chip DOM nodes across badge and empty pulses", () => {
    const [badge, setBadge] = createSignal<string | undefined>("1");
    const [empty, setEmpty] = createSignal(false);
    const [active, setActive] = createSignal(false);
    render(() => (
      <TabBar
        tabs={[
          {
            id: "progress",
            label: "Progress",
            badge: badge(),
            empty: empty(),
            active: active(),
            panel: () => <div />,
          },
          { id: "git", label: "Git", panel: () => <div /> },
        ]}
      />
    ));
    const chip = screen.getByTestId("tab-progress");
    expect(chip.querySelector(".tabs__chip-badge")?.textContent).toBe("1");

    setBadge("2");
    setEmpty(true);
    setActive(true);
    expect(screen.getByTestId("tab-progress")).toBe(chip);
    expect(chip.querySelector(".tabs__chip-badge")?.textContent).toBe("2");
    expect(chip.classList.contains("tabs__chip--empty")).toBe(true);
    expect(chip.classList.contains("tabs__chip--active")).toBe(true);

    fireEvent.click(chip);
    expect(chip.getAttribute("aria-expanded")).toBe("true");
  });

  it("a drag on the rail moves the window without selecting the tab", () => {
    const onOpenChange = vi.fn();
    render(() => <TabBar tabs={tabs} open={null} onOpenChange={onOpenChange} />);
    const chip = screen.getByTestId("tab-progress");
    dragRail(chip.parentElement as HTMLElement, 30);
    fireEvent.click(chip);
    expect(onOpenChange).not.toHaveBeenCalled();
  });

  it("a tap on a chip (no drag) still selects it under custom chrome", () => {
    const onOpenChange = vi.fn();
    render(() => <TabBar tabs={tabs} open={null} onOpenChange={onOpenChange} />);
    const chip = screen.getByTestId("tab-git");
    dragRail(chip.parentElement as HTMLElement, 0);
    fireEvent.click(chip);
    expect(onOpenChange).toHaveBeenCalledWith("git");
  });

  it("mousedown on a chip does not steal focus from another element", () => {
    render(() => <TabBar tabs={tabs} />);
    const input = document.createElement("textarea");
    document.body.appendChild(input);
    input.focus();
    fireEvent.mouseDown(screen.getByTestId("tab-progress"));
    expect(document.activeElement).toBe(input);
    document.body.removeChild(input);
  });

  it("starts retracted when open with retracted prop", () => {
    render(() => (
      <TabBar tabs={tabs} open="progress" retracted onOpenChange={() => {}} />
    ));
    expect(screen.getByTestId("tabs-panel")).toBeTruthy();
    expect(screen.getByTestId("tab-progress").getAttribute("aria-expanded")).toBe(
      "true",
    );
    expect((screen.getByTestId("tabs-clip") as HTMLElement).style.height).toBe("0px");
  });

  it("expands the panel when retracted becomes false", () => {
    vi.useFakeTimers();
    stubResizeObserver();
    const [retracted, setRetracted] = createSignal(true);
    const panelHeight = 120;
    const onPanelHeightChange = vi.fn();
    render(() => (
      <TabBar
        tabs={tabs}
        open="progress"
        retracted={retracted()}
        onOpenChange={() => {}}
        onPanelHeightChange={onPanelHeightChange}
      />
    ));
    const clip = mockOpenPanelHeight(panelHeight);
    setRetracted(false);
    vi.runAllTimers();
    expect(clip.style.height === "auto" || clip.style.height.endsWith("px")).toBe(
      true,
    );
    expect(clip.style.height).not.toBe("0px");
    expect(onPanelHeightChange).toHaveBeenCalledWith(panelHeight);
  });

  it("slides the dock down by the settled panel height", () => {
    vi.useFakeTimers();
    stubResizeObserver();
    const [retracted, setRetracted] = createSignal(true);
    const panelHeight = 96;
    render(() => (
      <TabBar
        tabs={tabs}
        open="progress"
        retracted={retracted()}
        onOpenChange={() => {}}
        dock={<div data-testid="dock-row" />}
      />
    ));
    const dock = screen.getByTestId("dock-row").parentElement as HTMLElement;
    expect(dock.classList.contains("tabs__dock")).toBe(true);
    // The retracted panel leaves the dock in its header slot.
    expect(dock.style.transform).toBe("");
    mockOpenPanelHeight(panelHeight);
    setRetracted(false);
    vi.runAllTimers();
    expect(dock.style.transform).toBe(`translateY(${panelHeight}px)`);
    setRetracted(true);
    vi.runAllTimers();
    expect(dock.style.transform).toBe("");
  });
});
