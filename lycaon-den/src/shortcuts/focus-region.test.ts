// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  activeElementInFocusRegion,
  cycleFocusRegion,
  FOCUS_REGION_IDS,
  focusRegion,
  focusRegionContainsActive,
  isFocusRegionMounted,
  isRegionEntry,
  mountedFocusRegions,
  regionEntryTarget,
  registerFocusRegion,
  releaseFocusRegion,
  resetFocusRegionsForTests,
} from "./focus-region.ts";
import {
  resolveKeymap,
  DEFAULT_NAV_LEADER_CHORD,
  NAV_LEADER_ID,
} from "./keymap.ts";
import { contributionCommandAvailable } from "../contributions/dispatch.ts";
import { frameKeymapSource } from "../contributions/frame-keymap.ts";
import type { ShellFactState } from "../contributions/shell-facts.ts";
import {
  dispatchKeyboardEvent,
  pendingLeaderChord,
  registerCommandHandler,
  resetDispatcherForTests,
  setDispatcherPlatformForTests,
  setShortcutOverrides,
} from "./dispatcher.ts";
import {
  ALL_SHELL_FACTS,
  seedStockFrame,
  stockId,
} from "../contributions/stock-frame-test.ts";
import { STOCK_FRAME } from "../contributions/stock-frame.generated.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";

const BASE: ShellFactState = {
  ...ALL_SHELL_FACTS,
  activityLive: false,
  sessionIdle: true,
  mountedRegions: ["sidebar", "chat", "context", "composer"],
  contextFeatures: [
    "files",
    "search",
    "security",
    "artifacts",
    "blueprints",
    "extensions",
  ],
  isPeerWorkspace: false,
};

/** Availability of one stock command under an explicit fact state. */
function available(name: string, facts: Partial<ShellFactState> = {}): boolean {
  seedStockFrame({ ...BASE, ...facts });
  return contributionCommandAvailable(stockId(name));
}

describe("focus-region claims", () => {
  beforeEach(() => {
    resetFocusRegionsForTests();
  });

  afterEach(() => {
    document.body.replaceChildren();
    resetFocusRegionsForTests();
  });

  it("focuses the claimed element and ignores a stale release", () => {
    const first = document.createElement("div");
    const second = document.createElement("div");
    document.body.append(first, second);
    const focus = vi.spyOn(second, "focus");
    const claimA = {};
    const claimB = {};
    registerFocusRegion("chat", first, claimA);
    registerFocusRegion("chat", second, claimB);
    releaseFocusRegion("chat", claimA);
    expect(isFocusRegionMounted("chat")).toBe(true);
    expect(focusRegion("chat")).toBe(true);
    expect(focus).toHaveBeenCalledOnce();
    releaseFocusRegion("chat", claimB);
    expect(isFocusRegionMounted("chat")).toBe(false);
    expect(focusRegion("chat")).toBe(false);
  });

  it("lists mounted regions without DOM search fallbacks", () => {
    const el = document.createElement("button");
    document.body.append(el);
    registerFocusRegion("sidebar", el, {});
    expect(mountedFocusRegions()).toEqual(["sidebar"]);
  });

  it("detects when the caret sits inside a claimed region", () => {
    const files = document.createElement("div");
    files.tabIndex = 0;
    document.body.appendChild(files);
    const claimToken = {};
    registerFocusRegion("files", files, claimToken);
    files.focus();
    expect(activeElementInFocusRegion()).toBe(true);
    expect(activeElementInFocusRegion("files")).toBe(false);
    expect(activeElementInFocusRegion("composer")).toBe(true);
    expect(focusRegionContainsActive("files")).toBe(true);
    expect(focusRegionContainsActive("composer")).toBe(false);
    releaseFocusRegion("files", claimToken);
    files.remove();
  });

  it("treats a claimed region inside an idle resident surface as unmounted", () => {
    const host = document.createElement("div");
    host.setAttribute("data-resident", "idle");
    const settings = document.createElement("div");
    host.appendChild(settings);
    document.body.appendChild(host);
    const claimToken = {};
    registerFocusRegion("settings", settings, claimToken);
    expect(isFocusRegionMounted("settings")).toBe(false);
    expect(mountedFocusRegions()).toEqual([]);
    expect(focusRegion("settings")).toBe(false);
    host.setAttribute("data-resident", "pending");
    expect(isFocusRegionMounted("settings")).toBe(false);
    expect(focusRegion("settings")).toBe(false);
    host.setAttribute("data-resident", "active");
    expect(isFocusRegionMounted("settings")).toBe(true);
    expect(focusRegion("settings")).toBe(true);
    releaseFocusRegion("settings", claimToken);
    host.remove();
  });

  it("registers find and settings without go.* destinations", () => {
    expect(FOCUS_REGION_IDS).toContain("find");
    expect(FOCUS_REGION_IDS).toContain("settings");
    const find = document.createElement("div");
    document.body.append(find);
    registerFocusRegion("find", find, {});
    expect(isFocusRegionMounted("find")).toBe(true);
    expect(mountedFocusRegions()).toContain("find");
  });
});

describe("go.* availability", () => {
  it("hides Context destinations when the feature is off", () => {
    const ctxFacts: Partial<ShellFactState> = {
      contextFeatures: ["files", "search"],
    };
    expect(available("go-context-files", ctxFacts)).toBe(true);
    expect(available("go-context-security", ctxFacts)).toBe(false);
  });

  it("keeps catalog digits stable when sidebar prefs hide an entry", () => {
    // Visibility prefs are not on CommandContext — only feature availability.
    const ctxFacts: Partial<ShellFactState> = {
      contextFeatures: [
        "files",
        "search",
        "security",
        "artifacts",
        "blueprints",
        "extensions",
      ],
    };
    expect(available("go-context-files", ctxFacts)).toBe(true);
    expect(available("go-context-extensions", ctxFacts)).toBe(true);
  });

  it("requires a mounted chat/composer/files region", () => {
    const emptyFacts: Partial<ShellFactState> = {
      mountedRegions: [],
      chatNavigable: false,
      sidebarExpanded: false,
    };
    expect(available("go-chat", emptyFacts)).toBe(false);
    expect(available("go-composer", emptyFacts)).toBe(false);
    expect(available("go-files", emptyFacts)).toBe(false);
    expect(available("go-sidebar", emptyFacts)).toBe(false);
  });

  it("keeps chat destinations available while an inline stage controls the column", () => {
    const inlineStageFacts: Partial<ShellFactState> = {
      mountedRegions: ["sidebar", "context", "files"],
      chatNavigable: true,
    };
    expect(available("go-chat", inlineStageFacts)).toBe(true);
    expect(available("go-composer", inlineStageFacts)).toBe(true);
  });

  it("requires an expanded sidebar to focus it", () => {
    const ctxFacts: Partial<ShellFactState> = {
      sidebarExpanded: false,
      mountedRegions: ["sidebar"],
    };
    expect(available("go-sidebar", ctxFacts)).toBe(false);
  });

  it("omits go.context.* in a session peer without a mounted Context surface", () => {
    const ctxFacts: Partial<ShellFactState> = {
      contextNavigable: false,
      mountedRegions: ["chat", "composer"],
    };
    expect(available("go-context-files", ctxFacts)).toBe(false);
    expect(available("go-chat", ctxFacts)).toBe(true);
  });
});

describe("leader destination bindings", () => {
  it("defaults the leader to Mod+; and derives mnemonic destination sequences", () => {
    const map = resolveKeymap({}, frameKeymapSource(STOCK_FRAME, "macos"));
    expect(map.leaderChord).toBe(DEFAULT_NAV_LEADER_CHORD);
    expect(map.byCommand.get(stockId("go-context-files"))).toEqual(["Mod+; F"]);
    expect(map.byCommand.get(stockId("go-context-search"))).toEqual(["Mod+; K"]);
    expect(map.byCommand.get(stockId("go-context-security"))).toEqual(["Mod+; S"]);
    expect(map.byCommand.get(stockId("go-context-cost"))).toEqual(["Mod+; O"]);
    expect(map.byCommand.get(stockId("go-context-artifacts"))).toEqual(["Mod+; A"]);
    expect(map.byCommand.get(stockId("go-context-blueprints"))).toEqual(["Mod+; L"]);
    expect(map.byCommand.get(stockId("go-context-extensions"))).toEqual(["Mod+; E"]);
    expect(map.byCommand.get(stockId("go-files"))).toEqual(["Mod+; T"]);
    expect(map.byCommand.get(stockId("go-chat"))).toEqual(["Mod+; C"]);
    expect(map.byCommand.get(stockId("go-home"))).toEqual(["Mod+; H"]);
    expect(map.sequences.some((s) => s.commandId === stockId("go-sidebar"))).toBe(true);
  });

  it("rewrites default children when the leader override changes", () => {
    const map = resolveKeymap(
      { [NAV_LEADER_ID]: "Mod+'" },
      frameKeymapSource(STOCK_FRAME, "macos"),
    );
    expect(map.leaderChord).toBe("Mod+'");
    expect(map.byCommand.get(stockId("go-context-search"))).toEqual(["Mod+' K"]);
  });
});

describe("local navigation dispatch", () => {
  beforeEach(() => {
    resetDispatcherForTests();
    setDispatcherPlatformForTests("macos");
    seedStockFrame(BASE);
    setShortcutOverrides({});
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    resetDispatcherForTests();
    resetContributionStoreForTest();
  });

  it("enters pending on the default leader and runs go.chat", () => {
    const ran = vi.fn();
    registerCommandHandler("go.chat", ran);
    const leader = dispatchKeyboardEvent({
      key: ";",
      code: "Semicolon",
      metaKey: true,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    expect(leader.handled).toBe(true);
    expect(pendingLeaderChord()).toBe("Mod+;");

    const second = dispatchKeyboardEvent({
      key: "c",
      code: "KeyC",
      metaKey: false,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    expect(second.handled).toBe(true);
    expect(second.commandId).toBe(stockId("go-chat"));
    expect(ran).toHaveBeenCalledOnce();
  });

  it("consumes an unavailable context destination without invoking it", () => {
    seedStockFrame({ ...BASE, contextFeatures: ["files", "search"] });
    const ran = vi.fn();
    registerCommandHandler("go.context.security", ran);
    dispatchKeyboardEvent({
      key: ";",
      code: "Semicolon",
      metaKey: true,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    const second = dispatchKeyboardEvent({
      key: "s",
      code: "KeyS",
      metaKey: false,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
    });
    expect(second.handled).toBe(true);
    expect(second.commandId).toBeNull();
    expect(ran).not.toHaveBeenCalled();
  });
});

it("cycles leaf regions and skips inactive regions", () => {
  resetFocusRegionsForTests();
  const stage = document.createElement("section"), tree = document.createElement("button"), editor = document.createElement("textarea"), composer = document.createElement("textarea");
  stage.append(tree, editor); document.body.append(stage, composer);
  registerFocusRegion("context", stage, {}); registerFocusRegion("filesTree", tree, {});
  registerFocusRegion("files", editor, {}); registerFocusRegion("composer", composer, {});
  tree.focus(); expect(cycleFocusRegion(1)).toBe(true); expect(document.activeElement).toBe(editor);
  expect(cycleFocusRegion(-1)).toBe(true); expect(document.activeElement).toBe(tree);
  stage.setAttribute("inert", ""); cycleFocusRegion(1); expect(document.activeElement).toBe(composer);
  document.body.replaceChildren(); resetFocusRegionsForTests();
});

it("marks only focusRegion's own focus as region entry", () => {
  resetFocusRegionsForTests();
  const region = document.createElement("section");
  region.tabIndex = -1;
  document.body.append(region);
  registerFocusRegion("chat", region, {});
  const seen: boolean[] = [];
  region.addEventListener("focus", () => seen.push(isRegionEntry(region)));
  region.focus();
  region.blur();
  focusRegion("chat");
  expect(seen).toEqual([false, true]);
  expect(isRegionEntry(region)).toBe(false);
  document.body.replaceChildren(); resetFocusRegionsForTests();
});

it("picks region entry targets by priority, not document order", () => {
  const priority = ['.row[aria-current="true"]', ".row", "button"];
  const root = document.createElement("div");
  root.innerHTML = `
    <button class="hide">Hide sidebar</button>
    <div data-resident="idle"><button class="row" aria-current="true">Hidden chat</button></div>
    <button class="row other">Other chat</button>
    <button class="row current" aria-current="true">Current chat</button>`;
  document.body.append(root);
  expect(regionEntryTarget(root, priority)?.textContent).toBe("Current chat");
  root.querySelector(".current")!.remove();
  expect(regionEntryTarget(root, priority)?.textContent).toBe("Other chat");
  root.querySelector<HTMLButtonElement>(".other")!.disabled = true;
  expect(regionEntryTarget(root, priority)?.textContent).toBe("Hide sidebar");
  document.body.replaceChildren();
});
