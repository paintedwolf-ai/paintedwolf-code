import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { detectConflicts, resolveKeymap, type KeymapSource } from "./keymap.ts";
import { frameKeymapSource } from "../contributions/frame-keymap.ts";
import {
  seedStockFrame,
  stockBindingId,
  stockId,
} from "../contributions/stock-frame-test.ts";
import { STOCK_FRAME } from "../contributions/stock-frame.generated.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";
import type { TauriPlatform } from "../platform/runtime.ts";

const SEARCH_OPEN = stockId("search-open");
const HELP_SHORTCUTS = stockId("help-shortcuts");
const OVERLAY_DISMISS = stockId("overlay-dismiss");
const SESSION_NEW = stockId("session-new");
const NAV_TOGGLE = stockId("nav-toggle");
const LAUNCHER_OPEN = stockId("launcher-open");
const COMPOSER_SEND = stockId("composer-send");
const SEARCH_OPEN_BINDING = stockBindingId("search-open");
const HELP_SHORTCUTS_BINDING = stockBindingId("help-shortcuts");
const SESSION_NEW_BINDING = stockBindingId("session-new");
const NAV_TOGGLE_BINDING = stockBindingId("nav-toggle");
const LAUNCHER_OPEN_BINDING = stockBindingId("launcher-open");

function source(platform: TauriPlatform): KeymapSource {
  return frameKeymapSource(STOCK_FRAME, platform);
}

beforeEach(() => seedStockFrame());
afterEach(() => resetContributionStoreForTest());

describe("resolveKeymap", () => {
  it("falls through to declaration defaults when overrides are absent", () => {
    const map = resolveKeymap(null, source("macos"));
    expect(map.byCommand.get(SEARCH_OPEN)).toEqual(["Mod+K"]);
    expect(map.byCommand.get(HELP_SHORTCUTS)).toEqual(["?", "Mod+/"]);
  });

  it("lets an override replace all default chords for that command", () => {
    const map = resolveKeymap(
      {
        [SEARCH_OPEN_BINDING]: "Mod+Shift+F",
        [HELP_SHORTCUTS_BINDING]: "Mod+H",
      },
      source("linux"),
    );
    expect(map.byCommand.get(SEARCH_OPEN)).toEqual(["Mod+Shift+F"]);
    expect(map.byCommand.get(HELP_SHORTCUTS)).toEqual(["Mod+H"]);
    expect(map.byCommand.get(OVERLAY_DISMISS)).toEqual(["Escape"]);
  });

  it("drops ids outside the frame and unproducible chords", () => {
    const map = resolveKeymap(
      {
        "not.a.command": "Mod+X",
        [SEARCH_OPEN_BINDING]: "Mod+NotAKey",
        [SESSION_NEW_BINDING]: "Mod+N",
      },
      source("windows"),
    );
    expect(map.byCommand.get(SEARCH_OPEN)).toEqual(["Mod+K"]);
    expect(map.byCommand.has("not.a.command")).toBe(false);
    expect(map.byCommand.get(SESSION_NEW)).toEqual(["Mod+N"]);
  });

  it("normalizes override chord modifier order", () => {
    const map = resolveKeymap(
      { [NAV_TOGGLE_BINDING]: "Shift+Mod+B" },
      source("macos"),
    );
    expect(map.byCommand.get(NAV_TOGGLE)).toEqual(["Mod+Shift+B"]);
  });

  it("indexes resolved bindings by chord for O(1) lookup", () => {
    const map = resolveKeymap(null, source("macos"));
    const bindings = map.byChord.get("Mod+K");
    expect(bindings?.some((b) => b.commandId === SEARCH_OPEN)).toBe(true);
  });
});

describe("detectConflicts", () => {
  it("flags two same-scope commands on one chord", () => {
    const map = resolveKeymap(
      {
        [SEARCH_OPEN_BINDING]: "Mod+K",
        [LAUNCHER_OPEN_BINDING]: "Mod+K",
      },
      source("macos"),
    );
    const conflicts = detectConflicts(map).filter((c) => c.chord === "Mod+K");
    expect(conflicts).toHaveLength(1);
    const conflict = conflicts[0]!;
    expect(conflict.scope).toBe("global");
    expect([...conflict.commandIds].sort()).toEqual(
      [LAUNCHER_OPEN, SEARCH_OPEN].sort(),
    );
  });

  it("ignores cross-scope sharing of the same chord", () => {
    // Enter is composer.send (composer) and list.confirm (overlay) by default.
    const map = resolveKeymap(null, source("linux"));
    expect(detectConflicts(map).filter((c) => c.chord === "Enter")).toEqual([]);
    expect(map.byCommand.get(COMPOSER_SEND)).toContain("Enter");
  });

  it("does not flag one declaration's alternate chords as conflicts", () => {
    const map = resolveKeymap(null, source("windows"));
    expect(
      detectConflicts(map).filter((c) => c.commandIds.includes(HELP_SHORTCUTS)),
    ).toEqual([]);
  });
});
