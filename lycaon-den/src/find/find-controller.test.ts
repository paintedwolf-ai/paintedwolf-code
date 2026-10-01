// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  closeFind,
  editorCommandBarPlacement,
  findController,
  findEverywhereSeed,
  findNext,
  findPrev,
  isChatScopedFindable,
  openFind,
  toggleFind,
  registerFindableView,
  resetFindControllerForTests,
  setFindCaseSensitive,
  setFindQuery,
  setPrimaryFindableView,
  unregisterFindableView,
} from "./find-controller.ts";
import { registerFindRevealHost } from "./find-reveal.ts";
import { FIND_MARK_ATTR } from "./find-match.ts";

afterEach(() => {
  resetFindControllerForTests();
  document.body.replaceChildren();
});

describe("FindController", () => {
  it("registers a view, opens, matches, wraps next/prev, and closes", () => {
    const root = document.createElement("div");
    root.textContent = "alpha beta alpha";
    document.body.appendChild(root);
    const scroll = vi.fn();
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: scroll,
    });
    setPrimaryFindableView("stub");

    openFind();
    expect(findController.isOpen()).toBe(true);
    setFindQuery("alpha");
    expect(findController.matches()).toHaveLength(2);
    expect(findController.activeIndex()).toBe(0);
    expect(root.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`)).toHaveLength(2);
    expect(scroll).toHaveBeenCalled();

    findNext();
    expect(findController.activeIndex()).toBe(1);
    findNext();
    expect(findController.activeIndex()).toBe(0);
    findPrev();
    expect(findController.activeIndex()).toBe(1);

    closeFind();
    expect(findController.isOpen()).toBe(false);
    expect(root.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`)).toHaveLength(0);
  });

  it("empty query clears highlights", () => {
    const root = document.createElement("div");
    root.textContent = "needle";
    document.body.appendChild(root);
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    openFind();
    setFindQuery("needle");
    expect(findController.matches()).toHaveLength(1);
    setFindQuery("");
    expect(findController.matches()).toHaveLength(0);
    expect(root.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`)).toHaveLength(0);
  });

  it("case-sensitive toggle recomputes", () => {
    const root = document.createElement("div");
    root.textContent = "Foo foo";
    document.body.appendChild(root);
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    openFind();
    setFindQuery("foo");
    expect(findController.matches()).toHaveLength(2);
    setFindCaseSensitive(true);
    expect(findController.matches()).toHaveLength(1);
  });

  it("findNext updates active index without clearing the overlay", () => {
    const root = document.createElement("div");
    root.textContent = "alpha beta alpha";
    document.body.appendChild(root);
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    openFind();
    setFindQuery("alpha");
    expect(root.querySelector(`[data-den-find-overlay]`)).toBeTruthy();
    const overlayBefore = root.querySelector(`[data-den-find-overlay]`);
    findNext();
    expect(findController.activeIndex()).toBe(1);
    // Navigation updates the existing overlay.
    expect(root.querySelector(`[data-den-find-overlay]`)).toBe(overlayBefore);
  });

  it("findEverywhereSeed returns trimmed query", () => {
    setFindQuery("  seed  ");
    expect(findEverywhereSeed()).toBe("seed");
  });

  it("openFind is a no-op without a registered findable view", () => {
    openFind();
    expect(findController.isOpen()).toBe(false);
  });

  it("does not choose an arbitrary registered surface without focus or a primary", () => {
    const root = document.createElement("div");
    root.textContent = "needle";
    document.body.appendChild(root);
    registerFindableView({
      id: "background-surface",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });

    openFind();

    expect(findController.isOpen()).toBe(false);
  });

  it("keeps a newer view when a stale registration unregisters the same id", () => {
    const first = document.createElement("div");
    first.textContent = "first";
    const second = document.createElement("div");
    second.textContent = "second needle";
    document.body.append(first, second);
    const releaseFirst = registerFindableView({
      id: "shared",
      rootEl: () => first,
      scrollMatchIntoView: () => {},
    });
    const releaseSecond = registerFindableView({
      id: "shared",
      rootEl: () => second,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("shared");

    releaseFirst();
    openFind();
    setFindQuery("needle");

    expect(findController.matches()).toHaveLength(1);
    releaseSecond();
  });

  it("clears overlays from the departing active root", () => {
    const root = document.createElement("div");
    root.textContent = "needle";
    document.body.appendChild(root);
    registerFindableView({
      id: "departing",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("departing");
    openFind();
    setFindQuery("needle");
    expect(root.querySelector(`[${FIND_MARK_ATTR}]`)).toBeTruthy();

    unregisterFindableView("departing");

    expect(root.querySelector(`[${FIND_MARK_ATTR}]`)).toBeNull();
  });

  it("toggleFind closes when already open", () => {
    const root = document.createElement("div");
    root.textContent = "needle";
    document.body.appendChild(root);
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    toggleFind();
    expect(findController.isOpen()).toBe(true);
    toggleFind();
    expect(findController.isOpen()).toBe(false);
  });

  it("counts collapsed matches, expands on next, restores on close", () => {
    const root = document.createElement("div");
    const details = document.createElement("details");
    details.open = false;
    const body = document.createElement("p");
    body.textContent = "hidden-needle visible";
    details.appendChild(body);
    root.appendChild(details);
    document.body.appendChild(root);

    let collapsed = true;
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    registerFindRevealHost({
      id: "reveal-1",
      hostEl: () => details,
      isCollapsed: () => collapsed,
      revealForFind: () => {
        collapsed = false;
        details.open = true;
        return () => {
          collapsed = true;
          details.open = false;
        };
      },
    });

    openFind();
    setFindQuery("needle");
    expect(findController.matches().length).toBeGreaterThanOrEqual(1);
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);
    expect(details.open).toBe(false);

    findNext();
    expect(details.open).toBe(true);
    expect(findController.collapsedCount()).toBe(0);

    closeFind();
    expect(details.open).toBe(false);
  });

  it("drills nested collapsed hosts outermost-first then restores both", () => {
    const root = document.createElement("div");
    const outer = document.createElement("details");
    outer.open = false;
    const inner = document.createElement("details");
    inner.open = false;
    const body = document.createElement("p");
    body.textContent = "nested-needle";
    inner.appendChild(body);
    outer.appendChild(inner);
    root.appendChild(outer);
    document.body.appendChild(root);

    let outerCollapsed = true;
    let innerCollapsed = true;
    const expandOrder: string[] = [];
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    registerFindRevealHost({
      id: "outer",
      hostEl: () => outer,
      isCollapsed: () => outerCollapsed,
      revealForFind: () => {
        expandOrder.push("outer");
        outerCollapsed = false;
        outer.open = true;
        return () => {
          outerCollapsed = true;
          outer.open = false;
        };
      },
    });
    registerFindRevealHost({
      id: "inner",
      hostEl: () => inner,
      isCollapsed: () => innerCollapsed,
      revealForFind: () => {
        expandOrder.push("inner");
        innerCollapsed = false;
        inner.open = true;
        return () => {
          innerCollapsed = true;
          inner.open = false;
        };
      },
    });

    openFind();
    setFindQuery("nested-needle");
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);
    expect(outer.open).toBe(false);
    expect(inner.open).toBe(false);

    findNext();
    expect(expandOrder).toEqual(["outer", "inner"]);
    expect(outer.open).toBe(true);
    expect(inner.open).toBe(true);
    expect(findController.collapsedCount()).toBe(0);

    closeFind();
    expect(outer.open).toBe(false);
    expect(inner.open).toBe(false);
  });

  it("counts a hit collapsed when only the outer host is collapsed", () => {
    const root = document.createElement("div");
    const outer = document.createElement("details");
    outer.open = false;
    const inner = document.createElement("details");
    inner.open = true;
    const body = document.createElement("p");
    body.textContent = "outer-only-needle";
    inner.appendChild(body);
    outer.appendChild(inner);
    root.appendChild(outer);
    document.body.appendChild(root);

    let outerCollapsed = true;
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    registerFindRevealHost({
      id: "outer",
      hostEl: () => outer,
      isCollapsed: () => outerCollapsed,
      revealForFind: () => {
        outerCollapsed = false;
        outer.open = true;
        return () => {
          outerCollapsed = true;
          outer.open = false;
        };
      },
    });
    registerFindRevealHost({
      id: "inner",
      hostEl: () => inner,
      isCollapsed: () => false,
      revealForFind: () => () => {},
    });

    openFind();
    setFindQuery("outer-only-needle");
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);

    findNext();
    expect(outer.open).toBe(true);
    expect(findController.collapsedCount()).toBe(0);
  });

  it("places the editor command bar with its registered view", () => {
    expect(isChatScopedFindable("session-transcript")).toBe(true);
    expect(isChatScopedFindable("tool-body:tc-1")).toBe(true);
    expect(isChatScopedFindable("worker-transcript")).toBe(false);
    expect(isChatScopedFindable("worklog")).toBe(false);
    expect(editorCommandBarPlacement()).toBe("none");

    const root = document.createElement("div");
    root.textContent = "x";
    document.body.appendChild(root);
    registerFindableView({
      id: "session-transcript",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("session-transcript");
    openFind();
    expect(editorCommandBarPlacement()).toBe("composer");

    closeFind();
    unregisterFindableView("session-transcript");
    registerFindableView({
      id: "worklog",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("worklog");
    openFind();
    expect(editorCommandBarPlacement()).toBe("shell");

    closeFind();
    unregisterFindableView("worklog");
    registerFindableView({
      id: "files-editor",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("files-editor");
    openFind();
    expect(editorCommandBarPlacement()).toBe("files-editor");
  });
});
