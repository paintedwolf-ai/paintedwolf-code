import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  displayBinding,
  eventToBareKey,
  eventToChord,
  normalizeBinding,
  parseBinding,
  parseSequence,
  serializeSequence,
} from "./chord.ts";
import {
  DEFAULT_NAV_LEADER_CHORD,
  detectSequenceConflicts,
  leaderCollidesWithCatalog,
  leaderIsPrefix,
  resolveKeymap,
} from "./keymap.ts";
import {
  dispatchKeyboardEvent,
  pendingLeaderChord,
  registerCommandHandler,
  resetDispatcherForTests,
  setComposerFocused,
  setDispatcherPlatformForTests,
  setShortcutOverrides,
  shortcutBindingAvailable,
  shortcutCommandMatchesEvent,
} from "./dispatcher.ts";
import { NAV_LEADER_ID } from "./keymap.ts";
import { frameKeymapSource } from "../contributions/frame-keymap.ts";
import {
  seedStockFrame,
  stockBindingId,
  stockId,
} from "../contributions/stock-frame-test.ts";
import { STOCK_FRAME } from "../contributions/stock-frame.generated.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";

const SEARCH_OPEN = stockId("search-open");
const SESSION_NEW = stockId("session-new");
const GO_CHAT = stockId("go-chat");
const SEARCH_OPEN_BINDING = stockBindingId("search-open");
const SESSION_NEW_BINDING = stockBindingId("session-new");
const COMPOSER_SEND_BINDING = stockBindingId("composer-send");

const source = (platform: "macos" | "windows" | "linux") =>
  frameKeymapSource(STOCK_FRAME, platform);

describe("sequence binding parse/normalize", () => {
  it("round-trips Mod+; as a leader chord via physical Semicolon code", () => {
    const chord = eventToChord(
      {
        key: ";",
        code: "Semicolon",
        metaKey: true,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
      },
      "macos",
    );
    expect(chord).toBe("Mod+;");
    expect(normalizeBinding("Mod+;")).toBe("Mod+;");
  });

  it("parses and normalizes a two-step sequence", () => {
    expect(parseSequence("Mod+; C")).toEqual({
      leader: "Mod+;",
      secondKey: "C",
    });
    expect(normalizeBinding("Shift+Mod+; c")).toBe("Mod+Shift+; C");
    expect(serializeSequence("Mod+;", "1")).toBe("Mod+; 1");
  });

  it("rejects malformed sequences", () => {
    expect(parseSequence("Mod+;")).toBeNull();
    expect(parseSequence("Mod+; C D")).toBeNull();
    expect(parseSequence("Mod+; Mod+C")).toBeNull();
    expect(parseSequence("; C")).toBeNull(); // leader needs non-Shift modifier
    expect(normalizeBinding("Mod+; C D")).toBeNull();
  });

  it("eventToBareKey rejects modifiers and shifted physical keys", () => {
    expect(
      eventToBareKey({
        key: "c",
        code: "KeyC",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
      }),
    ).toBe("C");
    expect(
      eventToBareKey({
        key: "c",
        code: "KeyC",
        metaKey: true,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
      }),
    ).toBeNull();
    expect(
      eventToBareKey({
        key: "C",
        code: "KeyC",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: true,
      }),
    ).toBeNull();
    expect(
      eventToBareKey({
        key: ":",
        code: "Semicolon",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: true,
      }),
    ).toBeNull();
    expect(
      eventToBareKey({
        key: " ",
        code: "Space",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: true,
      }),
    ).toBeNull();
  });

  it("eventToBareKey preserves the produced question-mark key", () => {
    expect(
      eventToBareKey({
        key: "?",
        code: "Slash",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: true,
      }),
    ).toBe("?");
  });

  it("displayBinding shows two ordered steps", () => {
    expect(displayBinding("Mod+; C", "macos")).toBe("⌘; then C");
    expect(displayBinding("Mod+; C", "linux")).toBe("Ctrl+; then C");
  });
});

describe("resolveKeymap sequences", () => {
  beforeEach(() => seedStockFrame());
  afterEach(() => resetContributionStoreForTest());

  it("resolves nav.leader and accepts matching child sequences", () => {
    const map = resolveKeymap(
      {
        [NAV_LEADER_ID]: "Mod+;",
        [SEARCH_OPEN_BINDING]: "Mod+; S",
      },
      source("macos"),
    );
    expect(map.leaderChord).toBe("Mod+;");
    expect(map.byCommand.get(SEARCH_OPEN)).toEqual(["Mod+; S"]);
    expect(map.sequences.find((s) => s.commandId === SEARCH_OPEN)).toEqual({
      bindingId: SEARCH_OPEN_BINDING,
      commandId: SEARCH_OPEN,
      sequence: "Mod+; S",
      leader: "Mod+;",
      secondKey: "S",
      scope: "global",
    });
    expect(map.bindings.some((b) => b.commandId === SEARCH_OPEN)).toBe(false);
    expect(leaderIsPrefix(map, "Mod+;")).toBe(true);
  });

  it("drops foreign-prefix child overrides", () => {
    const map = resolveKeymap(
      {
        [NAV_LEADER_ID]: "Mod+;",
        [SEARCH_OPEN_BINDING]: "Mod+. S",
      },
      source("linux"),
    );
    expect(map.byCommand.get(SEARCH_OPEN)).toEqual(["Mod+K"]);
    expect(map.sequences.some((s) => s.commandId === SEARCH_OPEN)).toBe(false);
  });

  it("drops a corrupted Shift-only leader and retains the safe default", () => {
    const map = resolveKeymap({ [NAV_LEADER_ID]: "Shift+;" }, source("macos"));
    expect(map.leaderChord).toBe(DEFAULT_NAV_LEADER_CHORD);
    expect(map.byCommand.get(GO_CHAT)).toEqual(["Mod+; C"]);
  });

  it("allows the same sequence across scopes", () => {
    const map = resolveKeymap(
      {
        [NAV_LEADER_ID]: "Mod+;",
        [SEARCH_OPEN_BINDING]: "Mod+; Z",
        [COMPOSER_SEND_BINDING]: "Mod+; Z",
      },
      source("macos"),
    );
    const conflicts = detectSequenceConflicts(map).filter((c) => c.secondKey === "Z");
    expect(conflicts).toEqual([]);
  });

  it("detects same-scope sequence conflicts", () => {
    const map = resolveKeymap(
      {
        [NAV_LEADER_ID]: "Mod+;",
        [SEARCH_OPEN_BINDING]: "Mod+; Z",
        [SESSION_NEW_BINDING]: "Mod+; Z",
      },
      source("macos"),
    );
    const conflicts = detectSequenceConflicts(map).filter((c) => c.secondKey === "Z");
    expect(conflicts).toHaveLength(1);
    expect(conflicts[0]!.scope).toBe("global");
    expect(conflicts[0]!.leader).toBe("Mod+;");
    expect([...conflicts[0]!.commandIds].sort()).toEqual(
      [SEARCH_OPEN, SESSION_NEW].sort(),
    );
  });

  it("rejects a leader that collides with a catalog single chord", () => {
    expect(leaderCollidesWithCatalog("Mod+K", source("macos"))).toBe(true);
    expect(leaderCollidesWithCatalog("Mod+;", source("macos"))).toBe(false);
  });
});

describe("pending prefix lifecycle", () => {
  beforeEach(() => {
    resetDispatcherForTests();
    seedStockFrame();
    setDispatcherPlatformForTests("macos");
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    resetDispatcherForTests();
    resetContributionStoreForTest();
  });

  it("enters pending on the leader and expires at 1.5s", () => {
    setShortcutOverrides({ [NAV_LEADER_ID]: "Mod+;" });
    const result = dispatchKeyboardEvent({
      key: ";",
      code: "Semicolon",
      metaKey: true,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    expect(result.handled).toBe(true);
    expect(pendingLeaderChord()).toBe("Mod+;");

    vi.advanceTimersByTime(1499);
    expect(pendingLeaderChord()).toBe("Mod+;");

    vi.advanceTimersByTime(1);
    expect(pendingLeaderChord()).toBeNull();
  });

  it("consumes an invalid second key and clears pending", () => {
    setShortcutOverrides({ [NAV_LEADER_ID]: "Mod+;" });
    dispatchKeyboardEvent({
      key: ";",
      code: "Semicolon",
      metaKey: true,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    const second = dispatchKeyboardEvent({
      key: "z",
      code: "KeyZ",
      metaKey: false,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    expect(second.handled).toBe(true);
    expect(second.commandId).toBeNull();
    expect(pendingLeaderChord()).toBeNull();
  });

  it("blocks local command paths while a prefix is pending", () => {
    setShortcutOverrides({
      [NAV_LEADER_ID]: "Mod+;",
      [SEARCH_OPEN_BINDING]: "Q",
    });
    dispatchKeyboardEvent({
      key: ";",
      code: "Semicolon",
      metaKey: true,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });

    expect(shortcutBindingAvailable(SEARCH_OPEN_BINDING, "Q")).toBe(false);
    expect(
      shortcutCommandMatchesEvent(SEARCH_OPEN, {
        key: "q",
        code: "KeyQ",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
      }),
    ).toBe(false);
  });

  it("invokes a sequence child on a matching bare second key", () => {
    // Q is unclaimed in the stock catalog, so the override is the only child.
    setShortcutOverrides({
      [NAV_LEADER_ID]: "Mod+;",
      [SEARCH_OPEN_BINDING]: "Mod+; Q",
    });
    const ran = vi.fn();
    registerCommandHandler("search.open", ran);
    dispatchKeyboardEvent({
      key: ";",
      code: "Semicolon",
      metaKey: true,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    const second = dispatchKeyboardEvent({
      key: "q",
      code: "KeyQ",
      metaKey: false,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    expect(second.handled).toBe(true);
    expect(second.commandId).toBe(SEARCH_OPEN);
    expect(ran).toHaveBeenCalledOnce();
    expect(pendingLeaderChord()).toBeNull();
  });

  it("selects the highest active scope for a shared sequence", () => {
    setShortcutOverrides({
      [NAV_LEADER_ID]: "Mod+;",
      [SEARCH_OPEN_BINDING]: "Mod+; Q",
      [COMPOSER_SEND_BINDING]: "Mod+; Q",
    });
    const token = {};
    setComposerFocused(true, token);

    expect(shortcutBindingAvailable(SEARCH_OPEN_BINDING, "Mod+; Q")).toBe(
      false,
    );
    expect(shortcutBindingAvailable(COMPOSER_SEND_BINDING, "Mod+; Q")).toBe(
      true,
    );

    setComposerFocused(false, token);
    expect(shortcutBindingAvailable(SEARCH_OPEN_BINDING, "Mod+; Q")).toBe(true);
    expect(shortcutBindingAvailable(COMPOSER_SEND_BINDING, "Mod+; Q")).toBe(
      false,
    );
  });

  it("clears pending on Escape", () => {
    setShortcutOverrides({ [NAV_LEADER_ID]: "Mod+;" });
    dispatchKeyboardEvent({
      key: ";",
      code: "Semicolon",
      metaKey: true,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    const esc = dispatchKeyboardEvent({
      key: "Escape",
      code: "Escape",
      metaKey: false,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    expect(esc.handled).toBe(true);
    expect(pendingLeaderChord()).toBeNull();
  });

});

describe("parseBinding", () => {
  it("distinguishes chords from sequences", () => {
    expect(parseBinding("Mod+K")).toEqual({ kind: "chord", chord: "Mod+K" });
    expect(parseBinding("Mod+; C")).toEqual({
      kind: "sequence",
      leader: "Mod+;",
      secondKey: "C",
    });
  });
});
