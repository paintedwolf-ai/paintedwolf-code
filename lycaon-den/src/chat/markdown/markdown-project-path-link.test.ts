// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { markdownProjectPathLinkContextMenu } from "./markdown-project-path-link.ts";

describe("markdownProjectPathLinkContextMenu", () => {
  it("builds Open / Reveal / Copy / Add to chat for a jailed path button", () => {
    const button = document.createElement("button");
    button.setAttribute("data-den-source-path", "src/foo.ts");
    button.setAttribute("data-project-id", "p1");
    button.setAttribute("data-den-source-line", "12");
    document.body.appendChild(button);

    const event = new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
      clientX: 40,
      clientY: 60,
    });
    Object.defineProperty(event, "target", { value: button });
    const prevent = vi.spyOn(event, "preventDefault");

    const menu = markdownProjectPathLinkContextMenu(event, {
      projectId: "p1",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });

    expect(menu).not.toBeNull();
    expect(menu?.anchor).toEqual({ x: 40, y: 60 });
    expect(menu?.items.map((i) => i.testId)).toEqual([
      "path-menu-open",
      "path-menu-reveal-tree",
      "open-in-menu",
      "path-menu-copy-path",
      "path-menu-copy-relative-path",
      "menu-add-to-chat",
    ]);
    expect(prevent).toHaveBeenCalled();
    button.remove();
  });

  it("returns null when roots are missing", () => {
    const button = document.createElement("button");
    button.setAttribute("data-den-source-path", "src/foo.ts");
    button.setAttribute("data-project-id", "p1");
    const event = new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
    });
    Object.defineProperty(event, "target", { value: button });

    expect(
      markdownProjectPathLinkContextMenu(event, { projectId: "p1", rootRefs: [] }),
    ).toBeNull();
  });
});


it("keeps worker actions from using primary workspace paths", () => {
  const button = document.createElement("button");
  button.setAttribute("data-den-source-path", "src/foo.ts");
  button.setAttribute("data-project-id", "p1");
  button.setAttribute("data-den-source-job-id", "worker-1");
  button.setAttribute("data-den-navigation-reference", "foo.ts");
  const event = new MouseEvent("contextmenu", { cancelable: true });
  Object.defineProperty(event, "target", { value: button });
  const open = vi.fn();
  const menu = markdownProjectPathLinkContextMenu(event, {
    projectId: "p1", rootRefs: [{ id: "r", path: "/primary" }], onReferenceAction: open,
  });
  expect(menu?.items.find((item) => item.testId === "path-menu-copy-path")?.disabled).toBe(true);
  const openIn = menu?.items.find((item) => item.testId === "open-in-menu")?.submenu ?? [];
  expect(openIn.map((item) => item.testId)).toContain("open-in-file-manager");
  for (const item of openIn) expect(item.disabled).toBe(true);
  expect(menu?.items.some((item) => item.testId === "path-menu-reveal-tree")).toBe(false);
  expect(menu?.items.some((item) => item.testId === "menu-add-to-chat")).toBe(false);
  menu?.items.find((item) => item.testId === "path-menu-open")?.onSelect?.();
  expect(open).toHaveBeenCalledWith(button, "foo.ts", "open");
  expect(menu?.items.find((item) => item.testId === "path-menu-copy-relative-path")?.disabled).not.toBe(true);
});


it.each([false, true])("offers reference actions before and after a path is resolved: %s", (resolved) => {
  const button = document.createElement("button");
  button.setAttribute("data-den-navigation-reference", "reference-1");
  if (resolved) {
    button.setAttribute("data-den-source-path", "src/file.ts");
    button.setAttribute("data-project-id", "p1");
  }
  const event = new MouseEvent("contextmenu", { cancelable: true });
  Object.defineProperty(event, "target", { value: button });
  const action = vi.fn();
  const menu = markdownProjectPathLinkContextMenu(event, {
    projectId: "p1", rootRefs: [{ id: "root", path: "/repo" }], onReferenceAction: action,
  });
  menu?.items.find((item) => item.testId === "path-menu-reveal-tree")?.onSelect?.();
  expect(action).toHaveBeenLastCalledWith(button, "reference-1", "reveal");
  menu?.items.find((item) => item.testId === "path-menu-open")?.onSelect?.();
  expect(action).toHaveBeenLastCalledWith(button, "reference-1", "open");
});
