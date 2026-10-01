// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  dispatchKeyboardEvent,
  registerCommandHandler,
  resetDispatcherForTests,
  setDispatcherContextForTests,
  setDispatcherPlatformForTests,
} from "./dispatcher.ts";
import {
  seedStockFrame,
  stockId,
} from "../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";
import {
  commitNavCollapsedPref,
  navCollapsedPref,
  resetLayoutStoreForTests,
} from "../shell/layout-store.ts";

// Keep availability gates open in dispatch cases.
beforeEach(() => {
  seedStockFrame();
});

afterEach(() => {
  resetDispatcherForTests();
  setDispatcherPlatformForTests(null);
  resetLayoutStoreForTests();
  resetContributionStoreForTest();
});

function chordEvent(
  partial: Partial<{
    key: string;
    code: string;
    metaKey: boolean;
    ctrlKey: boolean;
    altKey: boolean;
    shiftKey: boolean;
    target: EventTarget | null;
  }>,
) {
  return {
    key: "a",
    code: "KeyA",
    metaKey: false,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    target: null,
    ...partial,
  };
}

describe("editor chords the frame controls", () => {
  // These bindings dispatch outside the editor keymap.
  const inFilesStage = () =>
    setDispatcherContextForTests(() => ({
      overlayActive: false,
      composerFocused: false,
      filesStageActive: true,
    }));

  it("files.save reaches its handler via Mod+S", () => {
    setDispatcherPlatformForTests("macos");
    inFilesStage();
    let saved = 0;
    registerCommandHandler("files.save", () => {
      saved += 1;
    });
    const result = dispatchKeyboardEvent(
      chordEvent({ key: "s", code: "KeyS", metaKey: true }),
    );
    expect(result.handled).toBe(true);
    expect(saved).toBe(1);
  });

  it("findings navigation reaches its handlers via F8 and Shift+F8", () => {
    setDispatcherPlatformForTests("macos");
    inFilesStage();
    const walked: string[] = [];
    registerCommandHandler("editor.nextFinding", () => walked.push("next"));
    registerCommandHandler("editor.previousFinding", () => walked.push("prev"));
    expect(
      dispatchKeyboardEvent(chordEvent({ key: "F8", code: "F8" })).handled,
    ).toBe(true);
    expect(
      dispatchKeyboardEvent(chordEvent({ key: "F8", code: "F8", shiftKey: true }))
        .handled,
    ).toBe(true);
    expect(walked).toEqual(["next", "prev"]);
  });
});

describe("new global command handlers", () => {
  it("nav.toggle flips navCollapsed via Mod+B", () => {
    setDispatcherPlatformForTests("macos");
    registerCommandHandler("nav.toggle", () => {
      commitNavCollapsedPref(!navCollapsedPref());
    });
    expect(navCollapsedPref()).toBe(false);
    const result = dispatchKeyboardEvent(
      chordEvent({ key: "b", code: "KeyB", metaKey: true }),
    );
    expect(result.handled).toBe(true);
    expect(navCollapsedPref()).toBe(true);
    dispatchKeyboardEvent(chordEvent({ key: "b", code: "KeyB", metaKey: true }));
    expect(navCollapsedPref()).toBe(false);
  });

  it("session.new / settings.open / launcher.open fire registered actions", () => {
    setDispatcherPlatformForTests("linux");
    const sessionNew = vi.fn();
    const settingsOpen = vi.fn();
    const launcherOpen = vi.fn();
    registerCommandHandler("session.new", sessionNew);
    registerCommandHandler("settings.open", settingsOpen);
    registerCommandHandler("launcher.open", launcherOpen);

    expect(
      dispatchKeyboardEvent(chordEvent({ key: "n", code: "KeyN", ctrlKey: true }))
        .commandId,
    ).toBe(stockId("session-new"));
    expect(sessionNew).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: ",", code: "Comma", ctrlKey: true }),
      ).commandId,
    ).toBe(stockId("settings-open"));
    expect(settingsOpen).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(chordEvent({ key: "p", code: "KeyP", ctrlKey: true }))
        .commandId,
    ).toBe(stockId("launcher-open"));
    expect(launcherOpen).toHaveBeenCalledOnce();
  });

  it("launcher.open fires from a textarea with allow_in_input", () => {
    setDispatcherPlatformForTests("macos");
    const launcherOpen = vi.fn();
    registerCommandHandler("launcher.open", launcherOpen);
    const textarea = document.createElement("textarea");
    document.body.appendChild(textarea);

    const result = dispatchKeyboardEvent(
      chordEvent({ key: "p", code: "KeyP", metaKey: true, target: textarea }),
    );
    expect(result.handled).toBe(true);
    expect(result.commandId).toBe(stockId("launcher-open"));
    expect(launcherOpen).toHaveBeenCalledOnce();

    textarea.remove();
  });

  it("Mod-chord globals fire from a textarea; bare ? does not", () => {
    setDispatcherPlatformForTests("macos");
    const search = vi.fn();
    const sessionNew = vi.fn();
    const settings = vi.fn();
    const nav = vi.fn();
    const help = vi.fn();
    registerCommandHandler("search.open", search);
    registerCommandHandler("session.new", sessionNew);
    registerCommandHandler("settings.open", settings);
    registerCommandHandler("nav.toggle", nav);
    registerCommandHandler("help.shortcuts", help);
    const textarea = document.createElement("textarea");
    document.body.appendChild(textarea);

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "k", code: "KeyK", metaKey: true, target: textarea }),
      ).commandId,
    ).toBe(stockId("search-open"));
    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "n", code: "KeyN", metaKey: true, target: textarea }),
      ).commandId,
    ).toBe(stockId("session-new"));
    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: ",", code: "Comma", metaKey: true, target: textarea }),
      ).commandId,
    ).toBe(stockId("settings-open"));
    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "b", code: "KeyB", metaKey: true, target: textarea }),
      ).commandId,
    ).toBe(stockId("nav-toggle"));
    expect(
      dispatchKeyboardEvent(
        chordEvent({
          key: "?",
          code: "Slash",
          shiftKey: true,
          target: textarea,
        }),
      ).handled,
    ).toBe(false);
    expect(help).not.toHaveBeenCalled();
    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "/", code: "Slash", metaKey: true, target: textarea }),
      ).commandId,
    ).toBe(stockId("help-shortcuts"));
    expect(help).toHaveBeenCalledOnce();

    textarea.remove();
  });

  it("session.stop / session.prev / session.next fire registered actions", () => {
    setDispatcherPlatformForTests("macos");
    const stop = vi.fn();
    const prev = vi.fn();
    const next = vi.fn();
    registerCommandHandler("session.stop", stop);
    registerCommandHandler("session.prev", prev);
    registerCommandHandler("session.next", next);

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: ".", code: "Period", metaKey: true }),
      ).commandId,
    ).toBe(stockId("session-stop"));
    expect(stop).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "ArrowUp", code: "ArrowUp", altKey: true }),
      ).commandId,
    ).toBe(stockId("session-prev"));
    expect(prev).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "ArrowDown", code: "ArrowDown", altKey: true }),
      ).commandId,
    ).toBe(stockId("session-next"));
    expect(next).toHaveBeenCalledOnce();
  });

  it("help.shortcuts is registered and handles ? / Mod+/", () => {
    setDispatcherPlatformForTests("macos");
    const openHelp = vi.fn();
    registerCommandHandler("help.shortcuts", openHelp);

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "?", code: "Slash", shiftKey: true }),
      ).handled,
    ).toBe(true);
    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "/", code: "Slash", metaKey: true }),
      ).handled,
    ).toBe(true);
    expect(openHelp).toHaveBeenCalledTimes(2);
  });
});
