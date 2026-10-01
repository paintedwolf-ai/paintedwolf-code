import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";
import {
  LIST_PANE_HEIGHT_MIN_PX,
  LIST_PANE_WIDTH_MAX_PX,
  NAV_WIDTH_DEFAULT_PX,
} from "../../../shared/app-state-types.ts";

describe("parseAppState layout", () => {
  it("preserves clamped nav width", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      layout: { navWidthPx: 320 },
    });
    expect(state.layout?.navWidthPx).toBe(320);
  });

  it("drops invalid nav width", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      layout: { navWidthPx: "wide" },
    });
    expect(state.layout).toBeUndefined();
  });

  it("clamps out-of-range nav width on load", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      layout: { navWidthPx: 9999 },
    });
    expect(state.layout?.navWidthPx).toBe(480);
  });

  it("preserves nav collapsed flag", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      layout: { navCollapsed: true },
    });
    expect(state.layout?.navCollapsed).toBe(true);
  });

  it("preserves the hidden-conversation flag and drops a malformed one", () => {
    expect(
      parseAppState({ version: 1, recents: [], layout: { hiddenSplitPane: "conversation" } })
        .layout?.hiddenSplitPane,
    ).toBe("conversation");
    expect(
      parseAppState({ version: 1, recents: [], layout: { hiddenSplitPane: "yes" } })
        .layout?.hiddenSplitPane,
    ).toBeUndefined();
  });

  it("preserves nav width and collapsed together", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      layout: { navWidthPx: 320, navCollapsed: true },
    });
    expect(state.layout?.navWidthPx).toBe(320);
    expect(state.layout?.navCollapsed).toBe(true);
  });

  it("defaults missing layout", () => {
    const state = parseAppState({ version: 1, recents: [] });
    expect(state.layout).toBeUndefined();
    expect(NAV_WIDTH_DEFAULT_PX).toBe(280);
  });

  it("preserves cached project summaries", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      cachedProjects: [
        {
          id: "p1",
          name: "Demo",
          roots: [
            {
              id: "r1",
              path: "/tmp/p",
              label: "p",
              is_primary: true,
              added_at: "2025-01-01T00:00:00Z",
              kind: "attached",
            },
          ],
          roots_generation: 0,
          session_count: 1,
          starred: false,
          is_draft: false,
          promotion: null,
          last_opened_at: "2025-01-01T00:00:00Z",
          created_at: "2025-01-01T00:00:00Z",
        },
      ],
    });
    expect(state.cachedProjects?.[0]?.id).toBe("p1");
  });

  it("rejects incomplete cached project summaries", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      cachedProjects: [
        {
          id: "p1",
          roots: [
            {
              id: "r1",
              path: "/tmp/p",
              label: "p",
              is_primary: true,
              added_at: "2025-01-01T00:00:00Z",
              kind: "attached",
            },
          ],
          roots_generation: 0,
          starred: false,
          is_draft: false,
          promotion: null,
          last_opened_at: "2025-01-01T00:00:00Z",
          created_at: "2025-01-01T00:00:00Z",
        },
      ],
    });
    expect(state.cachedProjects).toBeUndefined();
  });

  it("rejects cached roots without their host label", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      cachedProjects: [
        {
          id: "p1",
          name: "Demo",
          roots: [
            {
              id: "r1",
              path: "/tmp/p",
              is_primary: true,
              added_at: "2025-01-01T00:00:00Z",
              kind: "attached",
            },
          ],
          roots_generation: 0,
          session_count: 1,
          starred: false,
          is_draft: false,
          promotion: null,
          last_opened_at: "2025-01-01T00:00:00Z",
          created_at: "2025-01-01T00:00:00Z",
        },
      ],
    });
    expect(state.cachedProjects).toBeUndefined();
  });

  it("preserves a saved project after its last folder is detached", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      cachedProjects: [
        {
          id: "p1",
          name: "Notes",
          roots: [],
          roots_generation: 2,
          session_count: 1,
          starred: false,
          is_draft: false,
          promotion: null,
          last_opened_at: "2025-01-01T00:00:00Z",
          created_at: "2025-01-01T00:00:00Z",
        },
      ],
    });
    expect(state.cachedProjects?.[0]?.roots).toEqual([]);
  });

  it("preserves transcript row height sessions", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      transcriptRowHeights: [
        {
          projectId: "p1",
          sessionId: "s1",
          touchedAt: 100,
          rows: { "tool-1": { collapsed: 240, '["tool-1"]': 420 } },
        },
      ],
    });
    expect(state.transcriptRowHeights?.[0]?.rows["tool-1"]?.collapsed).toBe(240);
    expect(state.transcriptRowHeights?.[0]?.rows["tool-1"]?.['["tool-1"]']).toBe(
      420,
    );
  });

  it("preserves composer drafts keyed by session id", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      composerDrafts: {
        "sess-a": "half typed",
        "sess-b": "",
        "": "ignored",
      },
    });
    expect(state.composerDrafts).toEqual({ "sess-a": "half typed" });
  });

  it("preserves dismissed draft save offers per project", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      draftPromoteDismissed: ["proj-a", " proj-a ", "", 7, "proj-b"],
    });
    expect(state.draftPromoteDismissed).toEqual(["proj-a", "proj-b"]);
  });

  it("preserves dismissed no-folder banners per project", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      nofolderDismissed: ["proj-a", " proj-a ", "", 7, "proj-b"],
    });
    expect(state.nofolderDismissed).toEqual(["proj-a", "proj-b"]);
  });

  it("preserves per-surface list pane layout", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      layout: {
        listPanes: {
          security: {
            widthPx: 400,
            heightPx: 300,
            collapsed: true,
            columnWidths: { severity: 90, location: 170 },
            sortKey: "severity",
            sortDir: "desc",
          },
        },
      },
    });
    const pane = state.layout?.listPanes?.security;
    expect(pane?.widthPx).toBe(400);
    expect(pane?.heightPx).toBe(300);
    expect("collapsed" in (pane ?? {})).toBe(false);
    expect(pane?.columnWidths?.location).toBe(170);
    expect(pane?.sortKey).toBe("severity");
    expect(pane?.sortDir).toBe("desc");
  });

  it("clamps out-of-range pane sizes and drops an invalid sort direction", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      layout: {
        listPanes: {
          search: { widthPx: 9000, heightPx: 4, sortKey: "title", sortDir: "sideways" },
          "": { widthPx: 400 },
        },
      },
    });
    const pane = state.layout?.listPanes?.search;
    expect(pane?.widthPx).toBe(LIST_PANE_WIDTH_MAX_PX);
    expect(pane?.heightPx).toBe(LIST_PANE_HEIGHT_MIN_PX);
    expect(pane?.sortKey).toBeUndefined();
    expect(pane?.sortDir).toBeUndefined();
    expect(state.layout?.listPanes?.[""]).toBeUndefined();
  });
});
