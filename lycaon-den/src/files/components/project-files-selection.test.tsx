import { readSourceText } from "../../test/stylesheet-source.ts";

import { join } from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { selectionCopyTextAtTarget } from "../../platform/interaction/text-edit-context.ts";
import { createAppStore } from "../../store/app-state.ts";
import type { ProjectRoot } from "../../api/types.ts";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { resetProjectFilesForTests } from "../documents/project-files-buffers.ts";

const denSrc = join(import.meta.dirname, "../..");

function read(rel: string): string {
  return readSourceText(join(denSrc, rel), "utf8");
}

const ROOTS: ProjectRoot[] = [
  {
    id: "r1",
    path: "/repo",
    label: "repo",
    is_primary: true,
    added_at: "2026-01-01T00:00:00Z",
    kind: "attached",
  },
];

describe("Files stage selection discipline", () => {
  it("keeps one divider between the tabs and their overflow button", () => {
    const css = read("files-domain.css");
    const stripRule =
      css.match(/^\.den-files-tab-strip\s*\{([^}]*)\}/m)?.[1] ?? "";
    const tabRule = css.match(/\.den-files-tab__body\s*\{([^}]*)\}/)?.[1] ?? "";
    const activeRule =
      css.match(/\.den-files-tab--active \.den-files-tab__body\s*\{([^}]*)\}/)?.[1] ?? "";
    const overflowRule =
      css.match(/\.den-files-tabs-overflow\s*\{([^}]*)\}/)?.[1] ?? "";

    expect(stripRule).toMatch(/--den-files-tab-divider-width:\s*1px/);
    expect(tabRule).toMatch(
      /border-right:\s*var\(--den-files-tab-divider-width\) solid var\(--den-line\)/,
    );
    expect(activeRule).toMatch(/background:\s*var\(--den-current-window-selection,\s*var\(--den-selection\)\)/);
    expect(activeRule).toMatch(
      /background-image:\s*linear-gradient\([\s\S]*?var\(--den-current-window-caret,\s*var\(--den-accent\)\) 0 2px,[\s\S]*?transparent 2px/,
    );
    expect(overflowRule).toMatch(
      /border-left:\s*var\(--den-files-tab-divider-width\) solid var\(--den-line\)/,
    );
    expect(overflowRule).not.toMatch(/transform:/);
    const lastTabRule = css.match(
      /\.den-files-tabs-wrap:has\(\+ \.den-files-tabs-overflow\) \.den-files-tab:last-child \.den-files-tab__body\s*\{([^}]*)\}/,
    )?.[1] ?? "";
    expect(lastTabRule).toMatch(/border-right:\s*0/);
  });

  it("marks chrome unselectable and content selectable without !important", () => {
    const css = `${read("files-domain.css")}\n${read("files-utilities.css")}`;
    const selects = (name: string, mode: "none" | "text") =>
      new RegExp(`(?:\\.|@utility\\s+)${name}[\\s\\S]*?user-select:\\s*${mode}`);
    for (const name of [
      "den-files-tree__row",
      "den-files-tabs",
      "den-files-tabs-fade",
      "den-files-tabs-overflow",
      "den-files-pin-glyph",
      "den-files-open-list__row",
      "den-files-editor__status",
      "cm-gutters",
      "den-files-image__stage",
      "den-files-image__zoom",
      "den-files-image__img",
    ]) {
      expect(css, `${name} must be unselectable chrome`).toMatch(selects(name, "none"));
    }
    for (const name of [
      "cm-content",
      "den-files-editor__preview-body",
      "den-files-empty",
      "den-files-info__meta",
      "den-files-root-summary",
    ]) {
      expect(css, `${name} must be selectable content`).toMatch(selects(name, "text"));
    }
    // Static rules omit !important because drag state sets that priority.
    expect(css).not.toMatch(/user-select:[^;]+!important/);
  });

  it("does not offer Copy when chrome is right-clicked over a live selection", () => {
    const prose = document.createElement("p");
    prose.style.userSelect = "text";
    prose.textContent = "selected elsewhere";
    const chrome = document.createElement("div");
    chrome.className = "den-files-tree__row";
    chrome.style.userSelect = "none";
    chrome.textContent = "row";
    document.body.append(prose, chrome);
    const range = document.createRange();
    range.selectNodeContents(prose);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);
    expect(selectionCopyTextAtTarget(chrome)).toBeNull();
    prose.remove();
    chrome.remove();
  });
});

describe("ProjectFilesView stage chrome", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
  });

  it("keeps Files controls inside the sidebar", () => {
    const appStore = createAppStore();
    render(() => (
      <ProjectFilesView
        projectId="p1"
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));
    expect(document.querySelector(".den-browse-chrome__chips")).toBeNull();
    expect(document.querySelector(".den-browse-chrome")).toBeNull();
  });

  it("leaves routed-return chrome to the Shell titlebar", () => {
    const appStore = createAppStore();
    render(() => (
      <ProjectFilesView
        projectId="p1"
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));
    expect(screen.queryByTestId("stage-back")).toBeNull();
    expect(document.querySelector(".den-files-stage-gutter")).toBeNull();
    expect(document.querySelector(".den-browse-chrome")).toBeNull();
  });
});
