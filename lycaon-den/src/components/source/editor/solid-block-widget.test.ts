// @vitest-environment jsdom
import { expect, it } from "vitest";
import { EditorView } from "@codemirror/view";
import { createContext, createRoot, getOwner, onCleanup, useContext } from "solid-js";
import { SolidBlockViews, SolidBlockWidget, frameBlockViews, readBlockFrame } from "./solid-block-widget.ts";

const Named = createContext<string>();

it("renders its view with the surface's context and disposes it with the surface", () => {
  createRoot((disposeRoot) => {
    let scope = getOwner();
    let seen = "";
    Named.Provider({
      value: "surface",
      get children() {
        scope = getOwner();
        return null;
      },
    });
    const views = new SolidBlockViews(4);
    const widget = new SolidBlockWidget("section-a", views, scope, () => {
      seen = useContext(Named) ?? "none";
      return null;
    }, 24);
    const dom = widget.toDOM();
    expect(dom.className).toBe("cm-den-block");
    expect(seen).toBe("surface");
    views.destroy();
    disposeRoot();
  });
});

it("keeps a mounted view across rebuilds with the same key", () => {
  createRoot((dispose) => {
    const views = new SolidBlockViews(4);
    const first = new SolidBlockWidget("section-a", views, getOwner(), () => null, 24);
    expect(first.eq(new SolidBlockWidget("section-a", views, getOwner(), () => null, 24))).toBe(true);
    expect(first.eq(new SolidBlockWidget("section-b", views, getOwner(), () => null, 24))).toBe(false);
    views.destroy();
    dispose();
  });
});

it("keeps a block's view mounted after it scrolls out, and hands the same element back", () => {
  createRoot((dispose) => {
    const views = new SolidBlockViews(4);
    let builds = 0;
    const view = () => { builds += 1; return null; };
    const widget = new SolidBlockWidget("section-a", views, getOwner(), view, 24);

    const first = widget.toDOM();
    expect(builds).toBe(1);
    // The block scrolls away and back: the view was never torn down, so coming
    // back is an appendChild rather than a mount on the scrolling frame.
    widget.destroy();
    expect(widget.toDOM()).toBe(first);
    expect(builds).toBe(1);

    views.destroy();
    dispose();
  });
});

it("gives up the blocks parked longest once the budget is full", () => {
  createRoot((dispose) => {
    const views = new SolidBlockViews(1);
    const built: string[] = [], stopped: string[] = [];
    const widget = (key: string) => new SolidBlockWidget(key, views, getOwner(), () => {
      built.push(key);
      onCleanup(() => stopped.push(key));
      return null;
    }, 24);

    const a = widget("a"), b = widget("b");
    a.toDOM(); b.toDOM();
    expect(built).toEqual(["a", "b"]);

    // Both scroll away; one parked view fits the budget, so the block parked
    // longest is the one given up.
    a.destroy(); b.destroy();
    expect(stopped).toEqual(["a"]);

    // The block that was given up builds again; the kept one hands its
    // element back without building anything.
    built.length = 0;
    a.toDOM(); b.toDOM();
    expect(built).toEqual(["a"]);

    views.destroy();
    dispose();
  });
});

it("stops every view it holds when the surface goes away", () => {
  createRoot((dispose) => {
    const views = new SolidBlockViews(4);
    const stopped: string[] = [];
    const widget = new SolidBlockWidget("section-a", views, getOwner(), () => {
      onCleanup(() => stopped.push("section-a"));
      return null;
    }, 24);
    widget.toDOM();
    expect(stopped).toEqual([]);

    views.destroy();
    expect(stopped).toEqual(["section-a"]);
    dispose();
  });
});

it("reserves its height before the view renders", () => {
  const widget = new SolidBlockWidget("section-a", new SolidBlockViews(1), null, () => null, 42);
  expect(widget.estimatedHeight).toBe(42);
});

function frameOf(host: HTMLElement): { left: string; width: string } {
  return { left: host.style.getPropertyValue("--den-editor-block-left"), width: host.style.getPropertyValue("--den-editor-block-w") };
}

it("stamps the frame on every block it holds and on the blocks it mounts later", () => {
  createRoot((dispose) => {
    const views = new SolidBlockViews(4);
    const first = new SolidBlockWidget("section-a", views, getOwner(), () => null, 24).toDOM();
    expect(frameOf(first)).toEqual({ left: "", width: "" });

    expect(views.frame({ left: 56, width: 744 })).toBe(true);
    expect(frameOf(first)).toEqual({ left: "56px", width: "744px" });
    // The same frame again moves nothing, so nothing downstream reflows.
    expect(views.frame({ left: 56, width: 744 })).toBe(false);

    // A block parked off-document keeps following the frame; a block mounted afterwards starts in it.
    const parked = new SolidBlockWidget("section-b", views, getOwner(), () => null, 24);
    const second = parked.toDOM();
    parked.destroy();
    expect(views.frame({ left: 56, width: 600 })).toBe(true);
    expect(frameOf(second)).toEqual({ left: "56px", width: "600px" });
    expect(frameOf(new SolidBlockWidget("section-c", views, getOwner(), () => null, 24).toDOM())).toEqual({ left: "56px", width: "600px" });

    views.destroy();
    dispose();
  });
});

it("reads the frame as the scroller's width past its gutters, and nothing while the editor has no box", () => {
  const view = new EditorView({ doc: "one\n" });
  try {
    expect(readBlockFrame(view)).toBeUndefined();
    Object.defineProperty(view.scrollDOM, "clientWidth", { value: 800, configurable: true });
    Object.defineProperty(view.contentDOM, "offsetLeft", { value: 56, configurable: true });
    expect(readBlockFrame(view)).toEqual({ left: 56, width: 744 });
  } finally {
    view.destroy();
  }
});

it("frames the blocks when the editor measures, and again after a geometry change", () => {
  createRoot((dispose) => {
    const views = new SolidBlockViews(4);
    const host = new SolidBlockWidget("section-a", views, getOwner(), () => null, 24).toDOM();
    const view = new EditorView({ doc: "one\ntwo\n", extensions: frameBlockViews(views) });
    const measured = view as unknown as { measure: () => void };
    let width = 800;
    Object.defineProperty(view.scrollDOM, "clientWidth", { get: () => width, configurable: true });
    Object.defineProperty(view.contentDOM, "offsetLeft", { value: 56, configurable: true });
    try {
      measured.measure();
      expect(frameOf(host)).toEqual({ left: "56px", width: "744px" });

      // A document change carries the geometry flag, so its measure re-reads the narrower scroller.
      width = 600;
      view.dispatch({ changes: { from: 0, insert: "three\n" } });
      measured.measure();
      expect(frameOf(host)).toEqual({ left: "56px", width: "544px" });
    } finally {
      view.destroy();
      views.destroy();
      dispose();
    }
  });
});
