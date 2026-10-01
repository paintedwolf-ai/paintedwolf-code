import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  CONTEXT_NAV_CATALOG,
  EMPTY_APP_STATE_V1,
  type ContextNavItemId,
  DEFAULT_CONTEXT_NAV_VISIBLE,
} from "../../../shared/app-state-types.ts";
import {
  saveContextNavVisible,
  syncContextNavPrefsFromSnapshot,
} from "../../settings/editor/context-nav-prefs.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import {
  registerFocusRegion,
  releaseFocusRegion,
  resetFocusRegionsForTests,
} from "../../shortcuts/focus-region.ts";
import { ProjectContextZone } from "./ProjectContextZone.tsx";
import {
  requestedFirstTimeTips,
  resetFirstTimeTipRequestsForTests,
} from "../../first-time-tips/first-time-tips-service.ts";
import { resetFirstTimeTipsPrefsForTests } from "../../settings/system/first-time-tips-prefs.ts";

describe("ProjectContextZone", () => {
  const baseProps = {
    onOpenStage: vi.fn(),
    isStageActive: () => false,
    isStageAvailable: () => true,
  };

  beforeEach(async () => {
    resetFocusRegionsForTests();
    resetFirstTimeTipRequestsForTests();
    resetFirstTimeTipsPrefsForTests(undefined, false);
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    syncContextNavPrefsFromSnapshot();
    await saveContextNavVisible([...DEFAULT_CONTEXT_NAV_VISIBLE]);
  });

  it("opens Customize from the more cue and blocks nav while editing", () => {
    const onOpenStage = vi.fn();
    render(() => (
      <ProjectContextZone {...baseProps} onOpenStage={onOpenStage} />
    ));
    fireEvent.click(screen.getByTestId("project-context-customize"));
    expect(screen.getByTestId("project-context-grip-search")).toBeTruthy();
    expect(screen.getByTestId("project-context-grip-files")).toBeTruthy();
    expect(screen.getByTestId("project-context-grip-security")).toBeTruthy();
    expect(screen.getByTestId("project-context-grip-cost")).toBeTruthy();
    expect(screen.getByTestId("project-context-grip-blueprints")).toBeTruthy();
    fireEvent.click(screen.getByTestId("project-search-entry"));
    expect(onOpenStage).not.toHaveBeenCalled();
  });

  it("carries no per-entry placement mark — split is the shell's layout", () => {
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectId="p1"
        splitLive
        isStageActive={(id) => id === "files"}
      />
    ));
    const entry = screen.getByTestId("project-files-entry");
    expect(entry.getAttribute("data-first-time-tip-anchor")).toBe(
      "files-ai-editor",
    );
    expect(entry.querySelector(".den-layout-glyph")).toBeNull();
    expect(entry.getAttribute("aria-label")).toBe("Files");
    expect(entry.getAttribute("aria-current")).toBe("page");
  });

  it("introduces the Files tip when visible and still opens the stage on click", () => {
    const onOpenStage = vi.fn();
    resetFirstTimeTipsPrefsForTests({ enabled: true, dismissed: [] });
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectId="p1"
        onOpenStage={onOpenStage}
      />
    ));
    const entry = screen.getByTestId("project-files-entry");

    expect(requestedFirstTimeTips()).toEqual(["files-ai-editor"]);
    fireEvent.click(entry);
    expect(onOpenStage).toHaveBeenCalledOnce();
  });

  it("focuses the stage column and still reports a split re-click", () => {
    const onOpenStage = vi.fn();
    const token = {};
    const stage = document.createElement("div");
    stage.tabIndex = -1;
    stage.setAttribute("data-testid", "split-col-stage");
    document.body.appendChild(stage);
    registerFocusRegion("context", stage, token);
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectId="p1"
        splitLive
        isStageActive={(id) => id === "files"}
        onOpenStage={onOpenStage}
      />
    ));
    fireEvent.click(screen.getByTestId("project-files-entry"));
    expect(onOpenStage).toHaveBeenCalledOnce();
    expect(onOpenStage).toHaveBeenCalledWith("files");
    expect(document.activeElement).toBe(stage);
    releaseFocusRegion("context", token);
    stage.remove();
  });

  it("opens Files when entry is clicked", () => {
    const onOpenStage = vi.fn();
    render(() => (
      <ProjectContextZone {...baseProps} onOpenStage={onOpenStage} />
    ));
    fireEvent.click(screen.getByTestId("project-files-entry"));
    expect(onOpenStage).toHaveBeenCalledOnce();
    expect(onOpenStage).toHaveBeenCalledWith("files");
  });

  it("focuses a numbered project view from the sidebar picker", () => {
    const onFocusProjectView = vi.fn();
    const onCloseProjectView = vi.fn();
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectViews={[
          {
            label: "context:files:6",
            title: "Files",
            viewNumber: 6,
            kind: "context",
            projectId: "p1",
            stageId: "files",
          },
        ]}
        onFocusProjectView={onFocusProjectView}
        onCloseProjectView={onCloseProjectView}
      />
    ));
    fireEvent.click(screen.getByTestId("project-context-views"));
    fireEvent.click(screen.getByTestId("project-context-focus-window"));
    fireEvent.click(screen.getByTestId("project-context-focus-view-6"));
    expect(onFocusProjectView).toHaveBeenCalledWith("context:files:6");
    fireEvent.click(screen.getByTestId("project-context-views"));
    fireEvent.click(screen.getByTestId("project-context-close-window"));
    fireEvent.click(screen.getByTestId("project-context-close-view-6"));
    expect(onCloseProjectView).toHaveBeenCalledWith("context:files:6");
  });

  it("omits the project view picker without an available action", () => {
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectViews={[
          {
            label: "context:files:6",
            title: "Files",
            viewNumber: 6,
            kind: "context",
            projectId: "p1",
            stageId: "files",
          },
        ]}
      />
    ));
    expect(screen.queryByTestId("project-context-views")).toBeNull();
  });

  it("opens search when entry is clicked", () => {
    const onOpenStage = vi.fn();
    render(() => (
      <ProjectContextZone {...baseProps} onOpenStage={onOpenStage} />
    ));
    fireEvent.click(screen.getByTestId("project-search-entry"));
    expect(onOpenStage).toHaveBeenCalledOnce();
    expect(onOpenStage).toHaveBeenCalledWith("search");
  });

  it("hides Artifacts and Extensions by default", () => {
    render(() => <ProjectContextZone {...baseProps} />);
    expect(screen.getByTestId("project-security-entry")).toBeTruthy();
    expect(screen.getByTestId("project-cost-entry")).toBeTruthy();
    expect(screen.queryByTestId("project-artifacts-entry")).toBeNull();
    expect(screen.queryByTestId("project-extensions-entry")).toBeNull();
    expect(screen.getByTestId("project-search-entry")).toBeTruthy();
    expect(screen.getByTestId("project-files-entry")).toBeTruthy();
    expect(screen.getByTestId("project-blueprints-entry")).toBeTruthy();
    expect(screen.getByTestId("project-context-customize").textContent).toBe(
      "2 more",
    );
  });

  it("opens Customize from the more cue", () => {
    render(() => <ProjectContextZone {...baseProps} />);
    fireEvent.click(screen.getByTestId("project-context-customize"));
    expect(screen.getByTestId("project-context-show-artifacts")).toBeTruthy();
    expect(screen.queryByTestId("project-context-customize")).toBeNull();
    expect(screen.getByTestId("project-context-done")).toBeTruthy();
  });

  it("exits Customize from the bottom Done control", () => {
    render(() => <ProjectContextZone {...baseProps} />);
    fireEvent.click(screen.getByTestId("project-context-customize"));
    fireEvent.click(screen.getByTestId("project-context-done"));
    expect(screen.getByTestId("project-context-customize")).toBeTruthy();
    expect(screen.queryByTestId("project-context-done")).toBeNull();
  });

  it("shows Customize when every Context item is already pinned", async () => {
    await saveContextNavVisible([...CONTEXT_NAV_CATALOG]);
    render(() => <ProjectContextZone {...baseProps} />);
    expect(screen.getByTestId("project-context-customize").textContent).toBe(
      "Customize",
    );
    fireEvent.click(screen.getByTestId("project-context-customize"));
    expect(screen.getByTestId("project-context-done")).toBeTruthy();
  });

  it("shows hidden items in Customize → Add to context", async () => {
    render(() => <ProjectContextZone {...baseProps} />);
    fireEvent.click(screen.getByTestId("project-context-customize"));
    expect(screen.queryByTestId("project-context-customize")).toBeNull();
    expect(screen.getByTestId("project-context-show-artifacts")).toBeTruthy();
    expect(screen.getByTestId("project-context-show-extensions")).toBeTruthy();
    expect(screen.queryByTestId("project-context-show-security")).toBeNull();

    fireEvent.click(screen.getByTestId("project-context-show-artifacts"));
    expect(await screen.findByTestId("project-artifacts-entry")).toBeTruthy();
  });

  it("opens Security when shown and clicked", async () => {
    await saveContextNavVisible([
      "search",
      "files",
      "security",
      "blueprints",
    ]);
    const onOpenStage = vi.fn();
    render(() => (
      <ProjectContextZone {...baseProps} onOpenStage={onOpenStage} />
    ));
    fireEvent.click(screen.getByTestId("project-security-entry"));
    expect(onOpenStage).toHaveBeenCalledWith("security");
  });

  it("opens Cost when clicked", () => {
    const onOpenStage = vi.fn();
    render(() => (
      <ProjectContextZone {...baseProps} onOpenStage={onOpenStage} />
    ));
    fireEvent.click(screen.getByTestId("project-cost-entry"));
    expect(onOpenStage).toHaveBeenCalledWith("cost");
  });

  it("opens Artifacts when shown and clicked", async () => {
    await saveContextNavVisible([
      "search",
      "files",
      "artifacts",
      "blueprints",
    ]);
    const onOpenStage = vi.fn();
    render(() => (
      <ProjectContextZone {...baseProps} onOpenStage={onOpenStage} />
    ));
    fireEvent.click(screen.getByTestId("project-artifacts-entry"));
    expect(onOpenStage).toHaveBeenCalledWith("artifacts");
  });

  it("opens Blueprints when entry is clicked", () => {
    const onOpenStage = vi.fn();
    render(() => (
      <ProjectContextZone {...baseProps} onOpenStage={onOpenStage} />
    ));
    fireEvent.click(screen.getByTestId("project-blueprints-entry"));
    expect(onOpenStage).toHaveBeenCalledWith("blueprints");
  });

  it("grays disabled features under Add to context and keeps them out of the pin list", () => {
    const unavailable = new Set<ContextNavItemId>(["security", "extensions"]);
    render(() => (
      <ProjectContextZone
        {...baseProps}
        isStageAvailable={(id) => !unavailable.has(id)}
      />
    ));
    expect(screen.queryByTestId("project-security-entry")).toBeNull();
    expect(screen.queryByTestId("project-extensions-entry")).toBeNull();

    fireEvent.click(screen.getByTestId("project-context-customize"));
    expect(screen.getByTestId("project-context-disabled-security")).toBeTruthy();
    expect(
      screen.getByTestId("project-context-disabled-extensions"),
    ).toBeTruthy();
    expect(screen.queryByTestId("project-context-show-security")).toBeNull();
    expect(screen.queryByTestId("project-context-show-extensions")).toBeNull();
    expect(
      (screen.getByTestId("project-context-disabled-security") as HTMLButtonElement)
        .disabled,
    ).toBe(true);
  });

  it("opens Extensions when shown and clicked", async () => {
    await saveContextNavVisible([
      "search",
      "files",
      "extensions",
      "blueprints",
    ]);
    const onOpenStage = vi.fn();
    render(() => (
      <ProjectContextZone {...baseProps} onOpenStage={onOpenStage} />
    ));
    fireEvent.click(screen.getByTestId("project-extensions-entry"));
    expect(onOpenStage).toHaveBeenCalledWith("extensions");
  });

  it("detached entry shows the window glyph and locked aria-label, not the active dot", () => {
    const onOpenStage = vi.fn();
    const onRaiseWindow = vi.fn();
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectId="p1"
        isStageActive={(id) => id === "files"}
        detachedStages={new Set(["files"])}
        onOpenStage={onOpenStage}
        onRaiseWindow={onRaiseWindow}
        onOpenInNewWindow={vi.fn()}
      />
    ));
    const entry = screen.getByTestId("project-files-entry");
    expect(entry.getAttribute("aria-label")).toBe(
      "Files — open in its own window, bring to front",
    );
    expect(entry.getAttribute("aria-current")).toBeNull();
    expect(entry.classList.contains("project-context-zone__entry--active")).toBe(
      false,
    );
    expect(screen.getByTestId("project-context-detached-files")).toBeTruthy();
    fireEvent.click(entry);
    expect(onOpenStage).toHaveBeenCalledOnce();
    expect(onOpenStage).toHaveBeenCalledWith("files");
    expect(onRaiseWindow).not.toHaveBeenCalled();
  });

  // The navigation dot follows the displayed column, including a project stage.
  it("lands the travelling dot on the stage holding the column", async () => {
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectId="p1"
        isStageActive={(id) => id === "files"}
      />
    ));
    const list = screen.getByTestId("project-context-visible");
    const dot = list.querySelector<HTMLElement>(".den-nav-marker");
    expect(dot).toBeTruthy();
    await waitFor(() => expect(dot?.style.opacity).toBe("1"));

    const entries = list.querySelectorAll(".project-context-zone__entry");
    const filesIndex = [...entries].indexOf(
      screen.getByTestId("project-files-entry"),
    );
    expect(filesIndex).toBeGreaterThanOrEqual(0);
  });

  it("keeps the dot away from a stage that is detached to its own window", async () => {
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectId="p1"
        isStageActive={(id) => id === "files"}
        detachedStages={new Set<ContextNavItemId>(["files"])}
      />
    ));
    const dot = screen
      .getByTestId("project-context-visible")
      .querySelector<HTMLElement>(".den-nav-marker");
    await waitFor(() => expect(dot?.style.opacity).toBe("0"));
  });

  it("outside Tauri the pop-out context menu rows are absent", () => {
    render(() => (
      <ProjectContextZone
        {...baseProps}
        projectId="p1"
        onOpenInNewWindow={vi.fn()}
      />
    ));
    fireEvent.contextMenu(screen.getByTestId("project-files-entry"));
    expect(screen.queryByTestId("context-nav-open-window")).toBeNull();
  });
});
