import { readSourceText } from "../test/stylesheet-source.ts";

import { join } from "node:path";
import { describe, expect, it } from "vitest";

const denSrc = join(import.meta.dirname, "..");

function read(rel: string): string {
  return readSourceText(join(denSrc, rel), "utf8");
}

describe("spoken content + modal semantics", () => {
  it("keeps transcript prose paths user-selectable", () => {
    const bubbles = read("global-components.css");
    const md = read("markdown.css");
    expect(bubbles).toMatch(/\.bubble--user\s*\{[\s\S]*?user-select:\s*text/);
    expect(bubbles).toMatch(/\.bubble--assistant\s*\{[\s\S]*?user-select:\s*text/);
    expect(md).toMatch(/\.markdown-body\s*\{[\s\S]*?user-select:\s*text/);
    expect(md).toMatch(/\.markdown-body pre\s*\{[\s\S]*?user-select:\s*text/);
    expect(md).not.toMatch(/\.markdown-body\s*\{[\s\S]*?user-select:\s*none/);
  });

  it("keeps fenced code from trapping transcript wheel", () => {
    // Fences scroll in a sideways scrollport, whose viewport hands vertical gestures on.
    expect(read("chat/markdown/markdown-render.ts")).toMatch(
      /wrapInScrollportFrame\(pre[^)]*axis:\s*"x"/,
    );
    const sideways = read("platform/themed-scrollbars.css")
      .match(/\[data-den-scrollport="x"\] > \.den-scrollport__viewport\s*\{[^}]*\}/);
    expect(sideways?.[0]).toMatch(/overflow-x:\s*auto/);
    expect(sideways?.[0]).toMatch(/overflow-y:\s*hidden/);
    expect(sideways?.[0]).toMatch(/overscroll-behavior-y:\s*auto/);
  });

  it("marks workers and folders overlays as modal dialogs; search is a stage pane", () => {
    const search = read("components/search/GlobalSearchView.tsx");
    const workers = read("components/worker/WorkersDrawer.tsx");
    const worklog = read("components/worklog/WorklogPanel.tsx");
    const folders = read("components/WorkspaceFoldersPanel.tsx");

    // Search is a Shell stage pane (same nav model as settings), not a modal overlay.
    expect(search).not.toMatch(/role="dialog"/);
    expect(search).not.toMatch(/aria-modal="true"/);
    expect(search).not.toMatch(/createModalFocusTrap/);
    expect(search).not.toMatch(/den-dialog__/);
    expect(search).toMatch(/aria-label="Search"/);
    expect(search).toMatch(/BrowseStagePanel/);
    expect(search).toMatch(/global-search-view/);
    expect(search).not.toMatch(/den-search__footer/);

    // Both contextual drawers remain in their chat pane and do not claim global
    // modal semantics that would make the sibling split pane inaccessible.
    const contextDrawer = read("components/shell/ContextDrawer.tsx");
    expect(contextDrawer).toMatch(/role="dialog"/);
    expect(contextDrawer).not.toMatch(/aria-modal="true"/);
    expect(contextDrawer).toMatch(/aria-labelledby=\{props\.titleId\}/);
    expect(contextDrawer).toMatch(/class="den-context-drawer"/);
    expect(contextDrawer).not.toMatch(/Portal/);

    expect(workers).toMatch(/<ContextDrawer/);
    expect(workers).toMatch(/titleId="workers-drawer-title"/);

    expect(worklog).toMatch(/<ContextDrawer/);
    expect(worklog).toMatch(/titleId="worklog-drawer-title"/);
    expect(worklog).not.toMatch(/worklog-panel-backdrop/);
    expect(worklog).not.toMatch(/den-workers-drawer/);

    expect(folders).toMatch(/role="dialog"/);
    expect(folders).toMatch(/aria-modal="true"/);
    expect(folders).toMatch(/aria-labelledby="workspace-folders-title"/);
  });
});
