import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { STOCK_FRAME } from "../contributions/stock-frame.generated.ts";
import { frameKeymapSource } from "../contributions/frame-keymap.ts";
import { seedStockFrame, stockBindingId, stockId } from "../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";
import { NAV_LEADER_ID, resolveKeymap } from "./keymap.ts";
import { bindingAllowedOnPlatform, defaultBindingAllowedOnPlatform } from "./binding-policy.ts";
import {
  dispatchKeyboardEvent, pendingLeaderChord, registerCommandHandler,
  resetDispatcherForTests, setDispatcherPlatformForTests,
} from "./dispatcher.ts";

beforeEach(() => {
  resetDispatcherForTests();
  seedStockFrame();
  setDispatcherPlatformForTests("linux");
});
afterEach(() => {
  resetDispatcherForTests();
  resetContributionStoreForTest();
});

const event = (key: string, code: string, ctrlKey = false, shiftKey = false) => ({
  key, code, ctrlKey, shiftKey, altKey: false, metaKey: false,
});

describe("Linux desktop and toolkit-safe defaults", () => {
  it("keeps every affected command and the mnemonic namespace reachable", () => {
    const map = resolveKeymap({}, frameKeymapSource(STOCK_FRAME, "linux"));
    expect(map.byCommand.get(stockId("editor-select-all"))).toEqual(["Mod+A"]);
    expect(map.byCommand.get(stockId("files-move-tab-left"))).toEqual(["Mod+Shift+PageUp"]);
    expect(map.byCommand.get(stockId("files-move-tab-right"))).toEqual(["Mod+Shift+PageDown"]);
    expect(map.byCommand.get(stockId("session-stop"))).toEqual(["Shift+F5"]);
    expect(map.leaderChord).toBe("Mod+Shift+;");
    expect(map.byCommand.get(stockId("go-chat"))).toEqual(["Mod+Shift+; C"]);
  });

  it("preserves valid explicit person overrides", () => {
    const map = resolveKeymap({
      [NAV_LEADER_ID]: "Mod+Shift+F9",
      [stockBindingId("session-stop")]: "Mod+Shift+F10",
    }, frameKeymapSource(STOCK_FRAME, "linux"));
    expect(map.leaderChord).toBe("Mod+Shift+F9");
    expect(map.byCommand.get(stockId("go-chat"))).toEqual(["Mod+Shift+F9 C"]);
    expect(map.byCommand.get(stockId("session-stop"))).toEqual(["Mod+Shift+F10"]);
  });

  it("does not change macOS or Windows defaults", () => {
    for (const platform of ["macos", "windows"] as const) {
      const map = resolveKeymap({}, frameKeymapSource(STOCK_FRAME, platform));
      expect(map.leaderChord).toBe("Mod+;");
      expect(map.byCommand.get(stockId("session-stop"))).toEqual(["Mod+."]);
      expect(map.byCommand.get(stockId("files-move-tab-left"))).toEqual(["Mod+Alt+Shift+ArrowLeft"]);
    }
  });

  it("rejects desktop and emoji reservations for both stock and personal values", () => {
    for (const chord of ["Mod+Alt+A", "Mod+Alt+Shift+ArrowLeft", "Mod+Alt+Shift+ArrowRight", "Mod+.", "Mod+;"]) {
      expect(defaultBindingAllowedOnPlatform(chord, "linux"), chord).toBe(false);
      expect(bindingAllowedOnPlatform(chord, "linux"), chord).toBe(false);
    }
  });

  it("dispatches Stop without the native emoji chord", () => {
    const stop = vi.fn();
    registerCommandHandler(stockId("session-stop"), stop);
    expect(dispatchKeyboardEvent(event(".", "Period", true)).handled).toBe(false);
    expect(stop).not.toHaveBeenCalled();
    expect(dispatchKeyboardEvent(event("F5", "F5", false, true)).handled).toBe(true);
    expect(stop).toHaveBeenCalledOnce();
  });

  it("dispatches a shifted punctuation leader and its normal mnemonic child", () => {
    const chat = vi.fn();
    registerCommandHandler(stockId("go-chat"), chat);
    expect(dispatchKeyboardEvent(event(";", "Semicolon", true)).handled).toBe(false);
    expect(pendingLeaderChord()).toBeNull();
    expect(dispatchKeyboardEvent(event(":", "Semicolon", true, true)).handled).toBe(true);
    expect(pendingLeaderChord()).toBe("Mod+Shift+;");
    expect(dispatchKeyboardEvent(event("c", "KeyC")).handled).toBe(true);
    expect(chat).toHaveBeenCalledOnce();
    expect(pendingLeaderChord()).toBeNull();
  });
});
