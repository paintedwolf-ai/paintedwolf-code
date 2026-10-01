import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { createAppStore } from "../../store/app-state.ts";
import { BrowseStagePanel } from "./BrowseStagePanel.tsx";

describe("BrowseStagePanel", () => {
  it("renders the back affordance ahead of the title and wires onBack", () => {
    const appStore = createAppStore();
    const onBack = vi.fn();
    render(() => (
      <BrowseStagePanel
        appStore={appStore}
        requireClient={false}
        scrollHost="main"
        back={{ label: "Back to chat", onBack, testId: "files-back" }}
        title={<span>Files</span>}
      >
        <div>tree</div>
      </BrowseStagePanel>
    ));
    const back = screen.getByTestId("files-back");
    expect(back.textContent).toContain("Back to chat");
    const primary = back.closest(".den-browse-chrome__primary");
    expect(primary?.firstElementChild).toBe(back);
    fireEvent.click(back);
    expect(onBack).toHaveBeenCalledOnce();
  });

  it("renders no back affordance when back is absent", () => {
    const appStore = createAppStore();
    render(() => (
      <BrowseStagePanel
        appStore={appStore}
        requireClient={false}
        scrollHost="main"
        title={<span>Files</span>}
      >
        <div>tree</div>
      </BrowseStagePanel>
    ));
    expect(screen.queryByTestId("browse-chrome-back")).toBeNull();
  });

  it("renders chrome + children without requiring a client", () => {
    const appStore = createAppStore();
    render(() => (
      <BrowseStagePanel
        appStore={appStore}
        requireClient={false}
        scrollHost="main"
        title={<span>Artifacts</span>}
        primary={<span data-testid="stage-primary">filters</span>}
      >
        <div data-testid="stage-main">gallery</div>
      </BrowseStagePanel>
    ));
    expect(screen.getByTestId("browse-stage-panel")).toBeTruthy();
    expect(screen.getByTestId("browse-chrome")).toBeTruthy();
    expect(screen.getByTestId("stage-primary")).toBeTruthy();
    expect(screen.getByTestId("stage-main")).toBeTruthy();
    expect(
      screen
        .getByTestId("stage-main")
        .closest(".den-browse-main--scroll[data-den-scrollport] > .den-scrollport__viewport"),
    ).toBeTruthy();
    expect(screen.queryByTestId("stage-detail")).toBeNull();
  });

  it("holds the whole stage until its first body state settles", async () => {
    const appStore = createAppStore();
    const [contentReady, setContentReady] = createSignal(false);
    render(() => (
      <BrowseStagePanel
        appStore={appStore}
        requireClient={false}
        contentReady={contentReady}
        scrollHost="main"
      >
        <div>body</div>
      </BrowseStagePanel>
    ));

    const stage = screen.getByTestId("browse-stage-panel");
    expect(stage.getAttribute("data-boot")).toBe("pending");
    setContentReady(true);
    await vi.waitFor(() => expect(stage.getAttribute("data-boot")).toBe("ready"));
  });

  it("omits detail column when detail is null", () => {
    const appStore = createAppStore();
    const { container } = render(() => (
      <BrowseStagePanel
        appStore={appStore}
        requireClient={false}
        scrollHost="content"
        detail={null}
      >
        <div>list</div>
      </BrowseStagePanel>
    ));
    expect(container.querySelector(".den-browse-detail")).toBeNull();
    expect(container.querySelector(".den-browse-body--with-detail")).toBeNull();
    expect(container.querySelector(".den-browse-main--content-scroll")).toBeTruthy();
    expect(container.querySelector(".den-browse-main--scroll")).toBeNull();
  });

  it("shows detail when detail is set", () => {
    const appStore = createAppStore();
    const { container } = render(() => (
      <BrowseStagePanel
        appStore={appStore}
        requireClient={false}
        scrollHost="main"
        detail={<div data-testid="stage-detail">rule</div>}
      >
        <div>list</div>
      </BrowseStagePanel>
    ));
    expect(screen.getByTestId("stage-detail")).toBeTruthy();
    expect(container.querySelector(".den-browse-body--with-detail")).toBeTruthy();
  });
});
