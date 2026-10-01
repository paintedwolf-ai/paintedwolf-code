import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Show, createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../store/app-state-snapshot.ts";
import {
  resetFirstTimeTipsPrefsForTests,
  saveFirstTimeTipsEnabled,
} from "../../settings/system/first-time-tips-prefs.ts";
import {
  requestFirstTimeTip,
  resetFirstTimeTipRequestsForTests,
  requestedFirstTimeTips,
} from "../../first-time-tips/first-time-tips-service.ts";
import {
  FirstTimeTipsLayer,
  resolveFirstTimeTipPosition,
  scrollCanMoveFirstTimeTipAnchor,
} from "./FirstTimeTipsLayer.tsx";
import {
  beginShellLayoutBusy,
  endShellLayoutBusy,
  flushShellLayoutSettleForTests,
  resetShellLayoutBusyForTests,
} from "../../shell/shell-layout-busy.ts";
import {
  resetScrollActivityForTests,
  setupScrollActivity,
} from "../../platform/scrolling/scroll-activity.ts";

function rect(left: number, top: number, width: number, height: number): DOMRect {
  return {
    left,
    top,
    width,
    height,
    right: left + width,
    bottom: top + height,
  } as DOMRect;
}

describe("FirstTimeTipsLayer", () => {
  beforeEach(() => {
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(
      rect(100, 100, 20, 20),
    );
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetFirstTimeTipsPrefsForTests();
    resetFirstTimeTipRequestsForTests();
    resetShellLayoutBusyForTests();
    setupScrollActivity();
  });

  it("treats editor content scroll as unable to move a tip anchor", () => {
    const editor = document.createElement("div");
    editor.className = "cm-editor";
    const scroller = document.createElement("div");
    scroller.className = "cm-scroller";
    editor.appendChild(scroller);
    expect(scrollCanMoveFirstTimeTipAnchor(scroller)).toBe(false);
    expect(scrollCanMoveFirstTimeTipAnchor(editor)).toBe(false);
    expect(scrollCanMoveFirstTimeTipAnchor(document.createElement("div"))).toBe(
      true,
    );
    expect(scrollCanMoveFirstTimeTipAnchor(document)).toBe(true);
  });

  it("does not remeasure anchors when the editor scroller fires scroll", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-review-scope" type="button">
          Review scope
        </button>
        <div class="cm-editor">
          <div class="cm-scroller" data-testid="editor-scroller" />
        </div>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await screen.findByTestId("first-time-tip-files-review-scope");

    const measure = vi.mocked(HTMLElement.prototype.getBoundingClientRect);
    const before = measure.mock.calls.length;
    fireEvent.scroll(screen.getByTestId("editor-scroller"));
    fireEvent(screen.getByTestId("editor-scroller"), new Event("scrollend"));
    await Promise.resolve();
    expect(measure.mock.calls.length).toBe(before);
  });

  it("remeasures after a chrome scroll that can move the anchor", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-review-scope" type="button">
          Review scope
        </button>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await screen.findByTestId("first-time-tip-files-review-scope");

    const measure = vi.mocked(HTMLElement.prototype.getBoundingClientRect);
    const before = measure.mock.calls.length;
    fireEvent.scroll(document.body);
    fireEvent(document.body, new Event("scrollend"));
    await Promise.resolve();
    expect(measure.mock.calls.length).toBeGreaterThan(before);
  });

  it("holds the tip offscreen while a chrome scroll can carry its anchor", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-review-scope" type="button">
          Review scope
        </button>
        <div class="cm-editor"><div class="cm-scroller" data-testid="editor-scroller" /></div>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    const tip = await screen.findByTestId("first-time-tip-files-review-scope");
    const held = () => tip.classList.contains("den-first-time-tip--scroll-held");

    fireEvent.scroll(screen.getByTestId("editor-scroller"));
    expect(held()).toBe(false);
    fireEvent.scroll(document.body);
    expect(held()).toBe(true);
    // The scroller carries the mark; the document root never does.
    expect(document.documentElement.hasAttribute("data-den-scrolling")).toBe(false);
    fireEvent(document.body, new Event("scrollend"));
    expect(held()).toBe(false);
  });

  it("centers the preferred popout on its icon and flips when it cannot fit", () => {
    expect(
      resolveFirstTimeTipPosition(
        rect(100, 100, 20, 20),
        { width: 200, height: 100 },
        { width: 500, height: 500 },
        "bottom",
      ),
    ).toMatchObject({ left: 10, top: 130, placement: "bottom", arrowOffset: 100 });

    expect(
      resolveFirstTimeTipPosition(
        rect(200, 480, 20, 12),
        { width: 200, height: 100 },
        { width: 500, height: 500 },
        "bottom",
      ).placement,
    ).toBe("top");

    expect(
      resolveFirstTimeTipPosition(
        rect(480, 100, 20, 20),
        { width: 200, height: 100 },
        { width: 500, height: 500 },
        "right",
      ),
    ).toMatchObject({ placement: "left", arrowOffset: 50 });
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetFirstTimeTipsPrefsForTests(undefined, false);
    resetFirstTimeTipRequestsForTests();
    resetShellLayoutBusyForTests();
    resetScrollActivityForTests();
  });

  it("renders above the anchored eye and records acknowledgement", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-review-scope" type="button">
          Review scope
        </button>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");

    const tip = await screen.findByTestId("first-time-tip-files-review-scope");
    expect(tip.textContent).toContain("Choose what to compare");
    expect(tip.className).toContain("den-first-time-tip");
    expect(tip.className).toContain("den-stage-enter-fade");

    fireEvent.click(screen.getByTestId("first-time-tip-dismiss"));
    expect(getAppStateSnapshot().firstTimeTips?.dismissed).toEqual([
      "files-review-scope",
    ]);
  });

  it("moves focus into the tip and restores its anchor on Escape", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-review-scope" type="button">
          Review scope
        </button>
        <FirstTimeTipsLayer />
      </>
    ));
    const anchor = screen.getByText("Review scope");
    anchor.focus();
    requestFirstTimeTip("files-review-scope");

    const tip = await screen.findByRole("dialog", { name: "Choose what to compare" });
    const dismiss = screen.getByTestId("first-time-tip-dismiss");
    await waitFor(() => expect(document.activeElement).toBe(dismiss));
    expect(tip.getAttribute("aria-describedby")).toBeTruthy();

    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(anchor);
  });

  it("restores the first control inside a non-focusable anchor", async () => {
    render(() => (
      <>
        <button type="button">Outside</button>
        <div data-first-time-tip-anchor="project-search">
          <input aria-label="Search query" />
        </div>
        <FirstTimeTipsLayer />
      </>
    ));
    screen.getByRole("button", { name: "Outside" }).focus();
    requestFirstTimeTip("project-search");
    await screen.findByRole("dialog", {
      name: "Search connects code and project work",
    });

    fireEvent.keyDown(document, { key: "Escape" });

    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole("textbox", { name: "Search query" }),
      ),
    );
  });

  it("settles after positioning a mounted tip", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="selected-chat" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("selected-chat");

    const first = await screen.findByTestId("first-time-tip-selected-chat");
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(screen.getByTestId("first-time-tip-selected-chat")).toBe(first);
  });

  it("keeps the mounted tip through shell layout settlement", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="selected-chat" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("selected-chat");

    const tip = await screen.findByTestId("first-time-tip-selected-chat");
    beginShellLayoutBusy();
    endShellLayoutBusy();
    await flushShellLayoutSettleForTests();

    expect(screen.getByTestId("first-time-tip-selected-chat")).toBe(tip);
  });

  it("aims the visible arrow at the target center", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="project-search" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("project-search");

    const tip = await screen.findByTestId("first-time-tip-project-search");
    const arrow = await screen.findByTestId("first-time-tip-arrow");
    const placement = arrow.getAttribute("data-placement");
    expect(placement).toMatch(/^(top|right|bottom|left)$/);
    expect(tip.className).toContain(`den-first-time-tip--${placement}`);
    const tipOrigin =
      placement === "top" || placement === "bottom"
        ? Number.parseFloat(tip.style.left)
        : Number.parseFloat(tip.style.top);
    expect(
      Number.parseFloat(
        arrow.style.getPropertyValue("--den-first-time-tip-arrow-offset"),
      ),
    ).toBeCloseTo(110 - tipOrigin);
  });

  it("does not render while the General preference is disabled", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-review-scope" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    await saveFirstTimeTipsEnabled(false);
    requestFirstTimeTip("files-review-scope");

    await Promise.resolve();
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();
  });

  it("uses rollout priority before advancing to the next visible tip", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-review-scope" type="button" />
        <button data-first-time-tip-anchor="selected-chat" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    requestFirstTimeTip("selected-chat");

    await screen.findByTestId("first-time-tip-selected-chat");
    fireEvent.click(screen.getByTestId("first-time-tip-dismiss"));
    expect(
      await screen.findByTestId("first-time-tip-files-review-scope"),
    ).toBeTruthy();
  });

  it("keeps a pending tip through a pane change until it is acknowledged", async () => {
    const [filesVisible, setFilesVisible] = createSignal(true);
    render(() => (
      <>
        <Show when={filesVisible()}>
          <button data-first-time-tip-anchor="files-review-scope" type="button" />
        </Show>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await screen.findByTestId("first-time-tip-files-review-scope");

    setFilesVisible(false);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();
    expect(requestedFirstTimeTips()).toContain("files-review-scope");

    setFilesVisible(true);
    await screen.findByTestId("first-time-tip-files-review-scope");
    fireEvent.click(screen.getByTestId("first-time-tip-dismiss"));
    expect(requestedFirstTimeTips()).not.toContain("files-review-scope");
  });

  it("ignores concealed handoff anchors and returns when the anchor is ready", async () => {
    const [concealed, setConcealed] = createSignal(false);
    render(() => (
      <>
        <div aria-hidden={concealed() ? "true" : undefined}>
          <button data-first-time-tip-anchor="files-review-scope" type="button" />
        </div>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await screen.findByTestId("first-time-tip-files-review-scope");

    setConcealed(true);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();
    expect(requestedFirstTimeTips()).toContain("files-review-scope");

    setConcealed(false);
    await screen.findByTestId("first-time-tip-files-review-scope");
  });

  it("holds tips while an ancestor is booting and reveals when ready", async () => {
    const [boot, setBoot] = createSignal<"pending" | "ready">("pending");
    render(() => (
      <>
        <div data-boot={boot()}>
          <button data-first-time-tip-anchor="files-review-scope" type="button" />
        </div>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();

    setBoot("ready");
    await screen.findByTestId("first-time-tip-files-review-scope");
  });

  it("holds tips while an ancestor is preparing presentation and reveals when published", async () => {
    const [presentation, setPresentation] = createSignal<"preparing" | "published">("preparing");
    render(() => (
      <>
        <div data-presentation={presentation()}>
          <button data-first-time-tip-anchor="files-review-scope" type="button" />
        </div>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();

    setPresentation("published");
    await screen.findByTestId("first-time-tip-files-review-scope");
  });

  it("holds tips while an ancestor is busy and reveals when settled", async () => {
    const [busy, setBusy] = createSignal(true);
    render(() => (
      <>
        <div aria-busy={busy() ? "true" : undefined}>
          <button data-first-time-tip-anchor="files-review-scope" type="button" />
        </div>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();

    setBusy(false);
    await screen.findByTestId("first-time-tip-files-review-scope");
  });

  it("holds tips while an ancestor is retained and reveals when published", async () => {
    const [retained, setRetained] = createSignal(true);
    render(() => (
      <>
        <div data-retained={retained() ? "true" : "false"}>
          <button data-first-time-tip-anchor="files-review-scope" type="button" />
        </div>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();

    setRetained(false);
    await screen.findByTestId("first-time-tip-files-review-scope");
  });

  it("holds tips while the workspace opening veil is active and reveals when veiled state ends", async () => {
    const [veiled, setVeiled] = createSignal(true);
    render(() => (
      <>
        <div
          class="den-shell-workspace-veil"
          classList={{ "den-shell-workspace-veil--hidden": !veiled() }}
          data-testid="shell-workspace-veil"
        />
        <button data-first-time-tip-anchor="files-review-scope" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();

    setVeiled(false);
    await screen.findByTestId("first-time-tip-files-review-scope");
  });

  it("disables all tips from the popout", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-review-scope" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-review-scope");
    await screen.findByTestId("first-time-tip-files-review-scope");

    fireEvent.click(screen.getByTestId("first-time-tip-disable"));
    expect(getAppStateSnapshot().firstTimeTips?.enabled).toBe(false);
    expect(screen.queryByTestId("first-time-tip-files-review-scope")).toBeNull();
    expect(requestedFirstTimeTips()).toContain("files-review-scope");

    await saveFirstTimeTipsEnabled(true);
    expect(
      await screen.findByTestId("first-time-tip-files-review-scope"),
    ).toBeTruthy();
  });

  it("shows the workflows introduction when its tab anchor is requested", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="workflows" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("workflows");

    expect(
      (await screen.findByTestId("first-time-tip-workflows")).textContent,
    ).toContain("Workflows guide a repeatable task");
  });

  it("introduces the AI-native Files editor when its navigation entry is requested", async () => {
    render(() => (
      <>
        <button data-first-time-tip-anchor="files-ai-editor" type="button" />
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-ai-editor");

    expect(
      (await screen.findByTestId("first-time-tip-files-ai-editor")).textContent,
    ).toContain("Files is a full code editor");
  });

  it("explains project roots from within the Files stage", async () => {
    render(() => (
      <>
        <section data-first-time-tip-anchor="files-project-roots">Project root</section>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip("files-project-roots");

    expect(
      (await screen.findByTestId("first-time-tip-files-project-roots")).textContent,
    ).toContain("Project roots organize your files");
  });

  it.each([
    ["project-search", "Search connects code and project work"],
    ["project-artifacts", "Artifacts keep useful outputs together"],
    ["project-security", "Security scans keep findings in context"],
    ["project-extensions", "Extensions shape this project"],
  ] as const)("introduces %s when its stage anchor is requested", async (id, title) => {
    render(() => (
      <>
        <section data-first-time-tip-anchor={id}>Stage</section>
        <FirstTimeTipsLayer />
      </>
    ));
    requestFirstTimeTip(id);

    expect((await screen.findByTestId(`first-time-tip-${id}`)).textContent).toContain(
      title,
    );
  });
});
