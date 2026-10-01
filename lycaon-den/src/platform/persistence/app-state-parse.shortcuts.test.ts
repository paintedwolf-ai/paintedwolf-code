import { describe, expect, it } from "vitest";
import { parseAppState, parseShortcuts } from "./app-state-parse.ts";

describe("parseShortcuts", () => {
  it("keeps declaration IDs with parseable chords", () => {
    expect(
      parseShortcuts({
        overrides: {
          "painted-wolf/platform:key-search-open": "Mod+J",
          "painted-wolf/platform:key-nav-toggle": "Shift+Mod+B",
        },
      }),
    ).toEqual({
      overrides: {
        "painted-wolf/platform:key-search-open": "Mod+J",
        "painted-wolf/platform:key-nav-toggle": "Mod+Shift+B",
      },
    });
  });

  it("keeps nav.leader and two-step sequence overrides", () => {
    expect(
      parseShortcuts({
        overrides: {
          "nav.leader": "Mod+;",
          "painted-wolf/platform:key-search-open": "Mod+; S",
        },
      }),
    ).toEqual({
      overrides: {
        "nav.leader": "Mod+;",
        "painted-wolf/platform:key-search-open": "Mod+; S",
      },
    });
  });

  it("drops invalid chords and keeps ids for resolve-time validation", () => {
    // Which ids exist is a property of the live frame, not of stored state, so
    // an unknown id survives parse and is dropped when the keymap resolves.
    expect(
      parseShortcuts({
        overrides: {
          "not.a.declaration": "Mod+X",
          "painted-wolf/platform:key-search-open": "Mod+NotAKey",
          "painted-wolf/platform:key-session-new": "Mod+N",
        },
      }),
    ).toEqual({
      overrides: {
        "not.a.declaration": "Mod+X",
        "painted-wolf/platform:key-session-new": "Mod+N",
      },
    });
  });

  it("drops malformed multi-step sequences", () => {
    expect(
      parseShortcuts({
        overrides: {
          "painted-wolf/platform:key-go-chat": "Mod+; C D",
          "painted-wolf/platform:key-go-sidebar": "Mod+; B",
        },
      }),
    ).toEqual({
      overrides: { "painted-wolf/platform:key-go-sidebar": "Mod+; B" },
    });
  });

  it("returns undefined for empty / junk shapes", () => {
    expect(parseShortcuts(null)).toBeUndefined();
    expect(parseShortcuts({ overrides: {} })).toBeUndefined();
    expect(parseShortcuts({ overrides: "nope" })).toBeUndefined();
  });
});

describe("parseAppState shortcuts", () => {
  it("round-trips a shortcuts slice", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      shortcuts: {
        overrides: {
          "painted-wolf/platform:key-launcher-open": "Mod+Shift+P",
        },
      },
    });
    expect(state.shortcuts?.overrides).toEqual({
      "painted-wolf/platform:key-launcher-open": "Mod+Shift+P",
    });
  });

  it("drops junk shortcut overrides on parse", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      shortcuts: {
        overrides: {
          "bogus.id": "???",
          "painted-wolf/platform:key-search-open": "???",
        },
      },
    });
    expect(state.shortcuts).toBeUndefined();
  });
});
