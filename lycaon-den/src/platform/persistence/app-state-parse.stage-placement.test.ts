import { describe, expect, it } from "vitest";
import type { DenLayoutPrefs } from "../../../shared/app-state-types.ts";
import {
  parseAppState,
  parseLayout,
} from "./app-state-parse.ts";
import { CHAT_COL_MIN } from "../../shell/stage-placement.ts";

describe("parseLayout placement", () => {
  it("validates and clamps", () => {
    const parsed = parseLayout({
      mode: "split",
      workspaceOrientation: "mirrored",
      chatWidthPx: 200,
    });

    expect(parsed?.mode).toBe("split");
    expect(parsed?.workspaceOrientation).toBe("mirrored");
    expect(parsed?.chatWidthPx).toBe(CHAT_COL_MIN);
    expect(parseLayout({ chatWidthPx: Number.NaN })).toBeUndefined();
  });

  it("accepts only declared split orders independently of mirroring", () => {
    for (const splitOrder of ["context-first", "chat-first"] as const) {
      expect(parseLayout({ splitOrder, workspaceOrientation: "mirrored" })).toEqual({
        splitOrder, workspaceOrientation: "mirrored",
      });
    }
    expect(parseLayout({ splitOrder: "left" })).toBeUndefined();
    expect(parseLayout({ splitOrder: true })).toBeUndefined();
  });

  it("keeps a launch companion only for a catalog stage", () => {
    expect(parseLayout({ startupCompanion: "files" })?.startupCompanion).toBe(
      "files",
    );
    expect(parseLayout({ startupCompanion: "settings" })).toBeUndefined();
    expect(parseLayout({ startupCompanion: 7 })).toBeUndefined();
  });

  it("keeps a split companion only for a catalog stage", () => {
    expect(parseLayout({ companion: "search" })?.companion).toBe("search");
    expect(parseLayout({ companion: "settings" })).toBeUndefined();
    expect(parseLayout({ companion: 7 })).toBeUndefined();
  });

  it("keeps a pinned narrow survivor only for a split column", () => {
    expect(parseLayout({ narrowSurvivor: "stage" })?.narrowSurvivor).toBe(
      "stage",
    );
    expect(
      parseLayout({ narrowSurvivor: "conversation" })?.narrowSurvivor,
    ).toBe("conversation");
    expect(parseLayout({ narrowSurvivor: "files" })).toBeUndefined();
    expect(parseLayout({ narrowSurvivor: true })).toBeUndefined();
  });

  it("rejects a mode that is not inline or split", () => {
    expect(parseLayout({ mode: "nope" })).toBeUndefined();
    expect(parseLayout({ mode: "side-by-side" })).toBeUndefined();
  });

  it("returns undefined for empty or invalid input", () => {
    expect(parseLayout(null)).toBeUndefined();
    expect(parseLayout([])).toBeUndefined();
    expect(parseLayout({ mode: "nope" })).toBeUndefined();
    expect(parseLayout({})).toBeUndefined();
  });
});

describe("parseAppState layout", () => {
  it("survives a full round trip", () => {
    const layout: DenLayoutPrefs = {
      navWidthPx: 336,
      navCollapsed: false,
      listPanes: { search: { widthPx: 420, sortKey: "title", sortDir: "asc" } },
      mode: "split",
      companion: "files",
      startupCompanion: "files",
      narrowSurvivor: "stage",
      workspaceOrientation: "standard",
      splitOrder: "chat-first",
      chatWidthPx: 440,
    };
    const state = parseAppState({
      version: 1,
      recents: [],
      layout,
    });
    expect(state.layout).toEqual(layout);
    expect(parseAppState(state).layout).toEqual(layout);
  });
});
