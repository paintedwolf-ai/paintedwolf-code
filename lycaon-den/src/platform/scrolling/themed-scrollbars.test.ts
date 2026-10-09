// @vitest-environment jsdom
import {
  construction,
  instance,
  frameHtml,
  makeFrame,
} from "./themed-scrollbars-test-harness.ts";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import { OverlayScrollbars } from "overlayscrollbars";
import { DEN_SCROLL_DIRECTION_ATTR, attachThemedViewportScrollbar, beginScrollMeasureQuiet, resetScrollMeasureQuietForTests, setupThemedScrollbars, syncThemedScrollbar } from "./themed-scrollbars.ts";
import { scrollportFrameParts, wrapInScrollportFrame } from "./scrollport-frame-dom.ts";
import { DEN_SCROLLPORT_AXIS_ATTR } from "./scrollport-frame-dom.ts";
import { bindOverlayScrollbarAutoHide } from "./overlay-scrollbar-autohide.ts";
import { bindOverlayScrollbarInput } from "./overlay-scrollbar-input.ts";

describe("themed scrollbars", () => {
  let stop: (() => void) | undefined;

  beforeEach(() => {
    vi.mocked(OverlayScrollbars).mockClear();
    vi.mocked(bindOverlayScrollbarInput).mockClear();
    vi.mocked(bindOverlayScrollbarAutoHide).mockClear();
    resetScrollMeasureQuietForTests();
  });

  afterEach(() => {
    stop?.();
    stop = undefined;
    document.body.replaceChildren();
    vi.useRealTimers();
  });

  it("attaches frames with their chrome outside the scroll container", async () => {
    vi.useFakeTimers();
    const root = document.createElement("section");
    root.innerHTML = `${frameHtml("y")}<div class="den-chat-stream"></div>`;
    document.body.appendChild(root);

    root.id = "root";
    stop = setupThemedScrollbars();
    await vi.runAllTimersAsync();
    const frame = root.children[0] as HTMLElement;
    const viewport = frame.firstElementChild as HTMLElement;
    const built = construction(frame);
    expect(built?.init.target).toBe(frame);
    expect(built?.init.elements?.viewport).toBe(viewport);
    expect(built?.options.overflow).toEqual({ x: "hidden", y: "scroll" });
    expect(instance(root.children[1] as HTMLElement)).toBeUndefined();
  });

  it("refuses markup that would put the chrome inside the scroll container", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      const root = document.createElement("div");
      root.id = "root";
      root.innerHTML = `<div class="den-scrollport" ${DEN_SCROLLPORT_AXIS_ATTR}="y"><p>rows</p></div>`;
      document.body.append(root);
      stop = setupThemedScrollbars();
      await vi.runAllTimersAsync();
      expect(instance(root.firstElementChild as HTMLElement)).toBeUndefined();
      expect(error).toHaveBeenCalledOnce();
    } finally {
      error.mockRestore();
    }
  });

  it("wraps built HTML in the same frame contract", () => {
    const holder = document.createElement("div");
    const pre = document.createElement("pre");
    holder.append(pre);
    const frame = wrapInScrollportFrame(pre, {
      frameClass: "markdown-code-scroll",
      axis: "x",
      defer: true,
      viewportAttributes: { tabindex: "0" },
    });
    expect(holder.firstElementChild).toBe(frame);
    expect(scrollportFrameParts(frame)).toEqual({
      axis: "x",
      viewport: frame.firstElementChild,
      content: pre,
    });
    expect((frame.firstElementChild as HTMLElement).getAttribute("tabindex")).toBe("0");
  });

  it.each(["data-resident", "data-resident-portal"])("defers hidden frames and retains bindings across %s switches", async (attribute) => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    const surface = document.createElement("section");
    surface.setAttribute(attribute, "idle");
    const { frame } = makeFrame();
    surface.append(frame);
    root.append(surface);
    document.body.append(root);
    stop = setupThemedScrollbars();
    await vi.runAllTimersAsync();
    expect(instance(frame)).toBeUndefined();
    surface.setAttribute(attribute, "active");
    await vi.runAllTimersAsync();
    const attached = instance(frame);
    expect(attached).toBeDefined();
    surface.setAttribute(attribute, "idle");
    await vi.runAllTimersAsync();
    expect(attached.sleep).toHaveBeenLastCalledWith(true);
    surface.setAttribute(attribute, "active");
    await vi.runAllTimersAsync();
    expect(instance(frame)).toBe(attached);
    expect(attached.destroy).not.toHaveBeenCalled();
    expect(attached.sleep).toHaveBeenLastCalledWith(false);
  });

  it.each(["initial", "added"])("bounds %s attachment work and discards removed pending frames", async (when) => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.append(root);
    if (when === "added") stop = setupThemedScrollbars();
    const frames = Array.from({ length: 10 }, () => {
      const { frame } = makeFrame();
      root.append(frame);
      return frame;
    });
    if (when === "initial") stop = setupThemedScrollbars();
    expect(frames.filter((frame) => instance(frame))).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(17);
    expect(frames.filter((frame) => instance(frame))).toHaveLength(4);
    frames[9]!.remove();
    await vi.runAllTimersAsync();
    expect(frames.filter((frame) => instance(frame))).toHaveLength(9);
    expect(instance(frames[9]!)).toBeUndefined();
  });

  it("yields after an expensive construction even before reaching the count bound", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    root.innerHTML = frameHtml().repeat(8);
    document.body.append(root);
    let clock = 0;
    const now = vi.spyOn(performance, "now").mockImplementation(() => clock += 5);
    try {
      stop = setupThemedScrollbars();
      await vi.advanceTimersByTimeAsync(17);
      expect([...root.children].filter((frame) => instance(frame as HTMLElement))).toHaveLength(1);
    } finally { now.mockRestore(); }
  });

  it("leaves unmarked content-specific browse layouts unchanged", async () => {
    vi.useFakeTimers();
    const root = document.createElement("section");
    root.innerHTML =
      '<div class="den-browse-main den-browse-main--content-scroll"><div class="den-files-stage"></div></div>';
    document.body.appendChild(root);

    root.id = "root";
    stop = setupThemedScrollbars();
    await vi.runAllTimersAsync();
    expect(instance(root.firstElementChild as HTMLElement)).toBeUndefined();
  });

  it("keeps auto-hide and scrollbar input out of the library", () => {
    const { frame, viewport } = makeFrame();
    document.body.appendChild(frame);

    syncThemedScrollbar(frame);

    const built = construction(frame);
    expect(built?.init.elements?.viewport).toBe(viewport);
    const options = built?.options as {
      showNativeOverlaidScrollbars?: boolean;
      scrollbars?: {
        autoHide?: string;
        autoHideDelay?: number;
        dragScroll?: boolean;
        clickScroll?: unknown;
      };
    };
    expect(options.showNativeOverlaidScrollbars).not.toBe(true);
    expect(options.scrollbars).toMatchObject({
      autoHide: "never",
      dragScroll: false,
      clickScroll: false,
    });
    expect(options.scrollbars).not.toHaveProperty("autoHideDelay");
    expect(bindOverlayScrollbarInput).toHaveBeenCalledTimes(1);
    expect(bindOverlayScrollbarAutoHide).toHaveBeenCalledTimes(1);
    expect(vi.mocked(bindOverlayScrollbarAutoHide).mock.calls[0]?.[0]).toMatchObject({
      host: frame,
      scrollOffsetElement: viewport,
    });
  });

  it("releases the auto-hide binding with the frame", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();
    const release = vi.fn();
    vi.mocked(bindOverlayScrollbarAutoHide).mockReturnValueOnce(release);

    const { frame } = makeFrame();
    root.appendChild(frame);
    await vi.runAllTimersAsync();
    expect(bindOverlayScrollbarAutoHide).toHaveBeenCalledTimes(1);

    frame.remove();
    await vi.runAllTimersAsync();
    expect(release).toHaveBeenCalledTimes(1);

    const controlledRelease = vi.fn();
    vi.mocked(bindOverlayScrollbarAutoHide).mockReturnValueOnce(controlledRelease);
    const dispose = attachThemedViewportScrollbar(
      document.createElement("div"),
      document.createElement("div"),
    );
    expect(bindOverlayScrollbarAutoHide).toHaveBeenCalledTimes(2);
    dispose();
    expect(controlledRelease).toHaveBeenCalledTimes(1);
  });

  it("drops held attachments when the manager stops", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    const release = beginScrollMeasureQuiet(root);
    const { frame } = makeFrame();
    root.appendChild(frame);
    await vi.runAllTimersAsync();
    expect(instance(frame)).toBeFalsy();

    stop();
    stop = undefined;
    release();
    await vi.runAllTimersAsync();
    expect(instance(frame)).toBeFalsy();
  });

  it("waits for a detached frame to mount", async () => {
    const { frame } = makeFrame();

    syncThemedScrollbar(frame);
    expect(instance(frame)).toBeUndefined();

    document.body.appendChild(frame);
    await Promise.resolve();

    expect(instance(frame)).toBeDefined();
  });

  it("discovers a late-mounted frame without rescanning existing frames", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    const existing = makeFrame("y", "home-view").frame;
    root.appendChild(existing);
    document.body.appendChild(root);
    stop = setupThemedScrollbars();
    await vi.runAllTimersAsync();
    const existingInstance = instance(existing);

    const added = makeFrame("y", "den-context-drawer-scroll").frame;
    root.appendChild(added);
    await vi.runAllTimersAsync();

    expect(instance(added)).toBeTruthy();
    expect(existingInstance.update).not.toHaveBeenCalled();
  });

  it("preserves an attached instance when its frame moves within the document", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    const first = document.createElement("section");
    const second = document.createElement("section");
    root.append(first, second);
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    const { frame } = makeFrame();
    first.appendChild(frame);
    await vi.runAllTimersAsync();
    const attached = instance(frame);

    second.appendChild(frame);
    await vi.runAllTimersAsync();

    expect(instance(frame)).toBe(attached);
    expect(attached.destroy).not.toHaveBeenCalled();
    expect(
      vi.mocked(OverlayScrollbars).mock.calls.filter((call) => call.length > 1),
    ).toHaveLength(1);
  });

  it("destroys a removed frame even when its discovery attribute was cleared", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    const { frame } = makeFrame();
    root.appendChild(frame);
    await vi.runAllTimersAsync();
    const attached = instance(frame);

    frame.removeAttribute(DEN_SCROLLPORT_AXIS_ATTR);
    frame.remove();
    await vi.runAllTimersAsync();

    expect(attached.destroy).toHaveBeenCalledOnce();
    expect(instance(frame)).toBeUndefined();
  });

  it("reconciles nested frames once when their subtree moves", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    const first = document.createElement("section");
    const second = document.createElement("section");
    root.append(first, second);
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    const subtree = document.createElement("article");
    subtree.innerHTML = frameHtml("y", "", frameHtml("x"));
    first.appendChild(subtree);
    await vi.runAllTimersAsync();
    const frames = [...subtree.querySelectorAll<HTMLElement>(`[${DEN_SCROLLPORT_AXIS_ATTR}]`)];
    expect(frames).toHaveLength(2);
    const attached = frames.map(instance);

    second.appendChild(subtree);
    await vi.runAllTimersAsync();

    expect(frames.map(instance)).toEqual(attached);
    for (const scrollbar of attached) {
      expect(scrollbar.destroy).not.toHaveBeenCalled();
    }
  });

  it("releases a discovered frame moved into an excluded surface", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    const source = document.createElement("section");
    const editor = document.createElement("section");
    editor.className = "cm-editor";
    root.append(source, editor);
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    const { frame } = makeFrame();
    source.appendChild(frame);
    await vi.runAllTimersAsync();
    const attached = instance(frame);

    editor.appendChild(frame);
    await vi.runAllTimersAsync();

    expect(attached.destroy).toHaveBeenCalledOnce();
    expect(instance(frame)).toBeUndefined();
  });

  it("never releases a frame its component attached when it leaves the document", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();
    const { frame, viewport } = makeFrame();
    root.appendChild(frame);
    const detach = attachThemedViewportScrollbar(frame, viewport, { axis: "y" });
    const attached = instance(frame);

    frame.remove();
    await vi.runAllTimersAsync();
    expect(attached.destroy).not.toHaveBeenCalled();
    detach();
    expect(attached.destroy).toHaveBeenCalledOnce();
  });

  it("uses an explicit direction fact for controller-managed viewports", () => {
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    document.body.appendChild(host);

    const detach = attachThemedViewportScrollbar(host, viewport);
    expect(host.getAttribute(DEN_SCROLL_DIRECTION_ATTR)).toBe("ltr");
    detach();
    expect(instance(host)).toBeUndefined();
  });
});
