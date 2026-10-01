// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  attachDispatcher,
  claimOverlayScope,
  claimShortcutBoundary,
  COMMAND_DECLINED,
  declinable,
  dispatchKeyboardEvent,
  invokeCommand,
  invokeShortcutBinding,
  isComposerFocused,
  isEditableTarget,
  registerCommandHandler,
  registerHostCommandInvoker,
  resetDispatcherForTests,
  setAskSendArmed,
  setComposerFocused,
  setDispatcherContextForTests,
  setDispatcherPlatformForTests,
  setShortcutCaptureActive,
  setShortcutOverrides,
  shortcutBindingAvailable,
  shortcutCommandMatchesEvent,
} from "./dispatcher.ts";
import {
  seedStockFrame,
  stockBindingId,
  stockId,
  ALL_SHELL_FACTS,
} from "../contributions/stock-frame-test.ts";
import { STOCK_FRAME } from "../contributions/stock-frame.generated.ts";
import { seedContributionFrameForTest, resetContributionStoreForTest } from "../contributions/contribution-store.ts";
import { setShellFactState } from "../contributions/shell-facts.ts";
import type { KeyboardEventLike } from "./chord.ts";

// Most cases use open availability.
beforeEach(() => {
  seedStockFrame();
});

afterEach(() => {
  resetDispatcherForTests();
  setDispatcherPlatformForTests(null);
  resetContributionStoreForTest();
});

function evt(
  partial: Partial<KeyboardEventLike> &
    Pick<KeyboardEventLike, "key" | "code"> & { target?: EventTarget | null },
): KeyboardEventLike & { target?: EventTarget | null } {
  return {
    metaKey: false,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    target: null,
    ...partial,
  };
}

describe("invokeCommand", () => {
  it("runs a registered handler", () => {
    const spy = vi.fn();
    registerCommandHandler("settings.open", spy);
    expect(invokeCommand("settings.open")).toBe("ran");
    expect(spy).toHaveBeenCalledOnce();
  });

  it("reports a missing handler", () => {
    expect(invokeCommand("settings.open")).toBe("missing");
  });

  it("reports a declining handler", () => {
    registerCommandHandler("settings.open", () => COMMAND_DECLINED);
    expect(invokeCommand("settings.open")).toBe("declined");
  });

  it("runs even while shortcut capture is active", () => {
    const spy = vi.fn();
    registerCommandHandler("session.new", spy);
    setShortcutCaptureActive(true);
    expect(invokeCommand("session.new")).toBe("ran");
    expect(spy).toHaveBeenCalledOnce();
  });

  it("runs overlay/composer commands without live scope context", () => {
    const confirm = vi.fn();
    const send = vi.fn();
    registerCommandHandler("list.confirm", confirm);
    registerCommandHandler("composer.send", send);
    setDispatcherContextForTests(() => ({
      overlayActive: false,
      composerFocused: false,
      filesStageActive: false,
    }));
    expect(invokeCommand("list.confirm")).toBe("ran");
    expect(invokeCommand("composer.send")).toBe("ran");
    expect(confirm).toHaveBeenCalledOnce();
    expect(send).toHaveBeenCalledOnce();
  });
});

describe("overlapping handler registrations", () => {
  it("the newest registration controls the id", () => {
    const first = vi.fn();
    const second = vi.fn();
    registerCommandHandler("composer.send", first);
    registerCommandHandler("composer.send", second);
    invokeCommand("composer.send");
    expect(second).toHaveBeenCalledOnce();
    expect(first).not.toHaveBeenCalled();
  });

  it("a crossfade replacement leaving does not remove the surviving registration", () => {
    // Entering instances may mount before exiting instances release.
    const staying = vi.fn();
    const leaving = vi.fn();
    registerCommandHandler("composer.send", staying);
    const detachLeaving = registerCommandHandler("composer.send", leaving);
    detachLeaving();
    expect(invokeCommand("composer.send")).toBe("ran");
    expect(staying).toHaveBeenCalledOnce();
    expect(leaving).not.toHaveBeenCalled();
  });

  it("releasing the last registration leaves the command unhandled", () => {
    const only = vi.fn();
    const detach = registerCommandHandler("composer.send", only);
    detach();
    expect(invokeCommand("composer.send")).toBe("missing");
  });

  it("a repeated release is inert", () => {
    const staying = vi.fn();
    const detachStaying = registerCommandHandler("composer.send", staying);
    const detachGone = registerCommandHandler("composer.send", vi.fn());
    detachGone();
    detachGone();
    expect(invokeCommand("composer.send")).toBe("ran");
    expect(staying).toHaveBeenCalledOnce();
    detachStaying();
  });
});

describe("isComposerFocused", () => {
  it("tracks the focused textarea, not a mounted composer", () => {
    const claimToken = {};
    expect(isComposerFocused()).toBe(false);
    setComposerFocused(true, claimToken);
    expect(isComposerFocused()).toBe(true);
    setComposerFocused(false, claimToken);
    expect(isComposerFocused()).toBe(false);
  });

  it("a second composer's blur does not clear the holder's focus", () => {
    const focused = {};
    setComposerFocused(true, focused);
    setComposerFocused(false, {});
    expect(isComposerFocused()).toBe(true);
    setComposerFocused(false, focused);
  });
});

describe("scope precedence", () => {
  it("prefers overlay over composer over global for the same chord", () => {
    setDispatcherPlatformForTests("macos");
    let context = {
      overlayActive: true,
      composerFocused: true,
      filesStageActive: false,
    };
    setDispatcherContextForTests(() => context);
    registerCommandHandler("list.confirm", vi.fn());
    registerCommandHandler("composer.send", vi.fn());

    expect(dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter" })).commandId)
      .toBe(stockId("list-confirm"));
    context = { ...context, overlayActive: false };
    expect(dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter" })).commandId)
      .toBe(stockId("composer-send"));
    context = { ...context, composerFocused: false };
    expect(dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter" })).commandId)
      .toBeNull();
  });

  it("Mod+W closes a Files tab only when the Files stage is active", () => {
    setDispatcherPlatformForTests("macos");
    let filesStageActive = true;
    setDispatcherContextForTests(() => ({
      overlayActive: false,
      composerFocused: false,
      filesStageActive,
    }));
    registerCommandHandler("files.closeTab", vi.fn());
    const event = evt({ key: "w", code: "KeyW", metaKey: true });

    expect(dispatchKeyboardEvent(event).commandId).toBe(stockId("files-close-tab"));
    filesStageActive = false;
    expect(dispatchKeyboardEvent(event).commandId).toBeNull();
  });
});

describe("availability gates the keyboard too", () => {
  it("drops a single chord whose `requires` does not hold", () => {
    // Unavailable commands stay out of keyboard dispatch.
    setDispatcherPlatformForTests("macos");
    seedStockFrame({ projectOpen: false });
    const sessionNew = vi.fn();
    registerCommandHandler("session.new", sessionNew);

    const result = dispatchKeyboardEvent(
      evt({ key: "n", code: "KeyN", metaKey: true }),
    );
    expect(result.commandId).toBeNull();
    expect(result.handled).toBe(false);
    expect(sessionNew).not.toHaveBeenCalled();

    seedStockFrame({ projectOpen: true });
    expect(
      dispatchKeyboardEvent(evt({ key: "n", code: "KeyN", metaKey: true }))
        .commandId,
    ).toBe(stockId("session-new"));
    expect(sessionNew).toHaveBeenCalledOnce();
  });

  it("leaves an ungated command alone", () => {
    setDispatcherPlatformForTests("macos");
    seedStockFrame();
    const search = vi.fn();
    registerCommandHandler("search.open", search);
    expect(
      dispatchKeyboardEvent(evt({ key: "k", code: "KeyK", metaKey: true }))
        .commandId,
    ).toBe(stockId("search-open"));
  });
});

describe("dispatchKeyboardEvent", () => {
  it("keeps source editing shortcuts out of other text inputs", () => {
    setDispatcherPlatformForTests("macos");
    setDispatcherContextForTests(() => ({ filesStageActive: true, overlayActive: false, composerFocused: false }));
    const select = vi.fn(); registerCommandHandler("editor.selectAll", select);
    const input = document.createElement("input");
    const content = document.createElement("div");
    content.className = "cm-content"; content.setAttribute("contenteditable", "true");
    expect(dispatchKeyboardEvent(evt({ key: "a", code: "KeyA", metaKey: true, target: input })).handled).toBe(false);
    expect(select).not.toHaveBeenCalled();
    expect(dispatchKeyboardEvent(evt({ key: "a", code: "KeyA", metaKey: true, target: content })).handled).toBe(true);
    expect(select).toHaveBeenCalledOnce();
  });

  it("keeps overlay scope until its last claim releases", () => {
    setDispatcherPlatformForTests("macos");
    const up = vi.fn();
    registerCommandHandler("list.up", up);
    const releaseFirst = claimOverlayScope();
    const releaseSecond = claimOverlayScope();

    releaseFirst();
    expect(
      dispatchKeyboardEvent(evt({ key: "ArrowUp", code: "ArrowUp" })).handled,
    ).toBe(true);

    releaseSecond();
    expect(
      dispatchKeyboardEvent(evt({ key: "ArrowUp", code: "ArrowUp" })).handled,
    ).toBe(false);
    expect(up).toHaveBeenCalledOnce();
  });

  it("leaves app shortcuts with the last standalone modal boundary", () => {
    setDispatcherPlatformForTests("macos");
    const search = vi.fn();
    registerCommandHandler("search.open", search);
    const releaseOuter = claimShortcutBoundary();
    const releaseInner = claimShortcutBoundary();

    expect(
      dispatchKeyboardEvent(evt({ key: "k", code: "KeyK", metaKey: true }))
        .handled,
    ).toBe(false);
    releaseOuter();
    expect(
      dispatchKeyboardEvent(evt({ key: "k", code: "KeyK", metaKey: true }))
        .handled,
    ).toBe(false);

    releaseInner();
    expect(
      dispatchKeyboardEvent(evt({ key: "k", code: "KeyK", metaKey: true }))
        .handled,
    ).toBe(true);
    expect(search).toHaveBeenCalledOnce();
  });

  it("fires search.open on Mod+K when handler registered", () => {
    setDispatcherPlatformForTests("macos");
    const spy = vi.fn();
    registerCommandHandler("search.open", spy);
    const result = dispatchKeyboardEvent(
      evt({ key: "k", code: "KeyK", metaKey: true }),
    );
    expect(result).toEqual({
      commandId: stockId("search-open"),
      chord: "Mod+K",
      handled: true,
    });
    expect(spy).toHaveBeenCalledOnce();
  });

  it("fires search.open on Ctrl+K on linux", () => {
    setDispatcherPlatformForTests("linux");
    const spy = vi.fn();
    registerCommandHandler("search.open", spy);
    const result = dispatchKeyboardEvent(
      evt({ key: "k", code: "KeyK", ctrlKey: true }),
    );
    expect(result.handled).toBe(true);
    expect(result.commandId).toBe(stockId("search-open"));
  });

  it("fires overlay.dismiss on Escape", () => {
    setDispatcherPlatformForTests("windows");
    const spy = vi.fn();
    registerCommandHandler("overlay.dismiss", spy);
    const result = dispatchKeyboardEvent(
      evt({ key: "Escape", code: "Escape" }),
    );
    expect(result.handled).toBe(true);
    expect(spy).toHaveBeenCalledOnce();
  });

  it("suppresses bare-character globals in a textarea unless allow_in_input", () => {
    setDispatcherPlatformForTests("macos");
    const search = vi.fn();
    const dismiss = vi.fn();
    const launcher = vi.fn();
    const sessionNew = vi.fn();
    const settings = vi.fn();
    const navToggle = vi.fn();
    const help = vi.fn();
    registerCommandHandler("search.open", search);
    registerCommandHandler("overlay.dismiss", dismiss);
    registerCommandHandler("launcher.open", launcher);
    registerCommandHandler("session.new", sessionNew);
    registerCommandHandler("settings.open", settings);
    registerCommandHandler("nav.toggle", navToggle);
    registerCommandHandler("help.shortcuts", help);

    const textarea = document.createElement("textarea");
    document.body.appendChild(textarea);

    // Mod-chord globals fire from inputs.
    expect(
      dispatchKeyboardEvent(
        evt({ key: "k", code: "KeyK", metaKey: true, target: textarea }),
      ).handled,
    ).toBe(true);
    expect(search).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(
        evt({ key: "p", code: "KeyP", metaKey: true, target: textarea }),
      ).handled,
    ).toBe(true);
    expect(launcher).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(
        evt({ key: "n", code: "KeyN", metaKey: true, target: textarea }),
      ).handled,
    ).toBe(true);
    expect(sessionNew).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(
        evt({ key: ",", code: "Comma", metaKey: true, target: textarea }),
      ).handled,
    ).toBe(true);
    expect(settings).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(
        evt({ key: "b", code: "KeyB", metaKey: true, target: textarea }),
      ).handled,
    ).toBe(true);
    expect(navToggle).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(evt({ key: "Escape", code: "Escape", target: textarea }))
        .handled,
    ).toBe(true);
    expect(dismiss).toHaveBeenCalledOnce();

    // Bare `?` still types without allow_in_input.
    expect(
      dispatchKeyboardEvent(
        evt({ key: "?", code: "Slash", shiftKey: true, target: textarea }),
      ).handled,
    ).toBe(false);
    expect(help).not.toHaveBeenCalled();

    // Mod+/ still opens help from an input.
    expect(
      dispatchKeyboardEvent(
        evt({ key: "/", code: "Slash", metaKey: true, target: textarea }),
      ).handled,
    ).toBe(true);
    expect(help).toHaveBeenCalledOnce();

    textarea.remove();
  });

  it("overlay list chords in an input: opted-in fire, Home/End stay with the caret", () => {
    setDispatcherPlatformForTests("macos");
    setDispatcherContextForTests(() => ({
      overlayActive: true,
      composerFocused: false,
      filesStageActive: false,
    }));
    const up = vi.fn();
    const first = vi.fn();
    const confirm = vi.fn();
    registerCommandHandler("list.up", up);
    registerCommandHandler("list.first", first);
    registerCommandHandler("list.confirm", confirm);

    const input = document.createElement("input");
    document.body.appendChild(input);

    expect(
      dispatchKeyboardEvent(
        evt({ key: "ArrowUp", code: "ArrowUp", target: input }),
      ).handled,
    ).toBe(true);
    expect(up).toHaveBeenCalledOnce();

    expect(
      dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter", target: input }))
        .handled,
    ).toBe(true);
    expect(confirm).toHaveBeenCalledOnce();

    // Home remains a caret key inside the filter.
    expect(
      dispatchKeyboardEvent(evt({ key: "Home", code: "Home", target: input }))
        .handled,
    ).toBe(false);
    expect(first).not.toHaveBeenCalled();

    const row = document.createElement("div");
    document.body.appendChild(row);
    expect(
      dispatchKeyboardEvent(evt({ key: "Home", code: "Home", target: row }))
        .handled,
    ).toBe(true);
    expect(first).toHaveBeenCalledOnce();

    input.remove();
    row.remove();
  });

  it("bare Enter/Space on a button yields to the native click", () => {
    setDispatcherPlatformForTests("macos");
    setDispatcherContextForTests(() => ({
      overlayActive: true,
      composerFocused: false,
      filesStageActive: false,
    }));
    const confirm = vi.fn();
    registerCommandHandler("list.confirm", confirm);

    const button = document.createElement("button");
    document.body.appendChild(button);

    // Enter remains a native button activation.
    expect(
      dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter", target: button }))
        .handled,
    ).toBe(false);
    expect(
      shortcutCommandMatchesEvent(
        stockId("list-confirm"),
        evt({ key: "Enter", code: "Enter", target: button }),
      ),
    ).toBe(false);
    expect(confirm).not.toHaveBeenCalled();

    // Modified chords still dispatch from buttons.
    const escalate = vi.fn();
    registerCommandHandler("search.open", escalate);
    expect(
      dispatchKeyboardEvent(
        evt({ key: "k", code: "KeyK", metaKey: true, target: button }),
      ).handled,
    ).toBe(true);
    expect(escalate).toHaveBeenCalledOnce();

    button.remove();
  });

  it("window listener ignores events a closer handler already consumed", () => {
    setDispatcherPlatformForTests("macos");
    const search = vi.fn();
    registerCommandHandler("search.open", search);
    const detach = attachDispatcher();

    const consumed = new KeyboardEvent("keydown", {
      key: "k",
      code: "KeyK",
      metaKey: true,
      cancelable: true,
    });
    consumed.preventDefault();
    window.dispatchEvent(consumed);
    expect(search).not.toHaveBeenCalled();

    const fresh = new KeyboardEvent("keydown", {
      key: "k",
      code: "KeyK",
      metaKey: true,
      cancelable: true,
    });
    window.dispatchEvent(fresh);
    expect(search).toHaveBeenCalledOnce();

    detach();
  });

  it("allows composer-scoped commands when composerFocused", () => {
    setDispatcherPlatformForTests("macos");
    setDispatcherContextForTests(() => ({
      overlayActive: false,
      composerFocused: true,
      filesStageActive: false,
    }));
    const send = vi.fn();
    registerCommandHandler("composer.send", send);
    const textarea = document.createElement("textarea");
    document.body.appendChild(textarea);

    const result = dispatchKeyboardEvent(
      evt({ key: "Enter", code: "Enter", target: textarea }),
    );
    expect(result.handled).toBe(true);
    expect(send).toHaveBeenCalledOnce();
    textarea.remove();
  });

  it("Enter on a choice control sends when ask send is armed", () => {
    setDispatcherPlatformForTests("macos");
    setDispatcherContextForTests(() => ({
      overlayActive: false,
      composerFocused: false,
      filesStageActive: false,
    }));
    const send = vi.fn();
    registerCommandHandler("composer.send", send);
    const claimToken = {};
    setAskSendArmed(true, claimToken);

    const radio = document.createElement("input");
    radio.type = "radio";
    document.body.appendChild(radio);

    const result = dispatchKeyboardEvent(
      evt({ key: "Enter", code: "Enter", target: radio }),
    );
    expect(result.commandId).toBe(stockId("composer-send"));
    expect(result.handled).toBe(true);
    expect(send).toHaveBeenCalledOnce();

    // Unarmed: same chord on a radio is a no-op (not composer-focused).
    setAskSendArmed(false, claimToken);
    send.mockClear();
    expect(
      dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter", target: radio }))
        .handled,
    ).toBe(false);
    expect(send).not.toHaveBeenCalled();
    radio.remove();
  });

  it("ask-armed Enter does not steal from overlays or non-choice targets", () => {
    setDispatcherPlatformForTests("macos");
    const send = vi.fn();
    const confirm = vi.fn();
    registerCommandHandler("composer.send", send);
    registerCommandHandler("list.confirm", confirm);
    const claimToken = {};
    setAskSendArmed(true, claimToken);

    setDispatcherContextForTests(() => ({
      overlayActive: true,
      composerFocused: false,
      filesStageActive: false,
    }));
    expect(
      dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter", target: null }))
        .commandId,
    ).toBe(stockId("list-confirm"));
    expect(send).not.toHaveBeenCalled();

    setDispatcherContextForTests(() => ({
      overlayActive: false,
      composerFocused: false,
      filesStageActive: false,
    }));
    const button = document.createElement("button");
    document.body.appendChild(button);
    expect(
      dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter", target: button }))
        .handled,
    ).toBe(false);
    expect(send).not.toHaveBeenCalled();
    button.remove();
  });

  it("returns commandId without handled when no handler registered", () => {
    setDispatcherPlatformForTests("macos");
    const result = dispatchKeyboardEvent(
      evt({ key: "k", code: "KeyK", metaKey: true }),
    );
    expect(result.commandId).toBe(stockId("search-open"));
    expect(result.handled).toBe(false);
  });

  it("honors overrides when resolving", () => {
    setDispatcherPlatformForTests("linux");
    setShortcutOverrides({ [stockBindingId("search-open")]: "Mod+J" });
    const spy = vi.fn();
    registerCommandHandler("search.open", spy);
    expect(
      dispatchKeyboardEvent(evt({ key: "k", code: "KeyK", ctrlKey: true }))
        .handled,
    ).toBe(false);
    expect(
      dispatchKeyboardEvent(evt({ key: "j", code: "KeyJ", ctrlKey: true }))
        .handled,
    ).toBe(true);
  });
});

describe("isEditableTarget", () => {
  it("detects input/textarea/contenteditable", () => {
    const input = document.createElement("input");
    const textarea = document.createElement("textarea");
    const div = document.createElement("div");
    div.setAttribute("contenteditable", "true");
    expect(isEditableTarget(input)).toBe(true);
    expect(isEditableTarget(textarea)).toBe(true);
    expect(isEditableTarget(div)).toBe(true);
    expect(isEditableTarget(document.createElement("div"))).toBe(false);
    expect(isEditableTarget(null)).toBe(false);
  });
});

describe("attachDispatcher", () => {
  it("registers a single window listener that preventDefaults on handle", () => {
    setDispatcherPlatformForTests("macos");
    const spy = vi.fn();
    registerCommandHandler("search.open", spy);
    const detach = attachDispatcher();

    const event = new KeyboardEvent("keydown", {
      key: "k",
      code: "KeyK",
      metaKey: true,
      bubbles: true,
      cancelable: true,
    });
    const prevented = !window.dispatchEvent(event);
    // preventDefault → dispatchEvent returns false when canceled
    expect(prevented || event.defaultPrevented).toBe(true);
    expect(spy).toHaveBeenCalledOnce();
    detach();
  });

  it("suppresses all commands while shortcut capture is active", () => {
    setDispatcherPlatformForTests("macos");
    const dismiss = vi.fn();
    const search = vi.fn();
    registerCommandHandler("overlay.dismiss", dismiss);
    registerCommandHandler("search.open", search);
    const detach = attachDispatcher();

    setShortcutCaptureActive(true);

    const escape = new KeyboardEvent("keydown", {
      key: "Escape",
      code: "Escape",
      bubbles: true,
      cancelable: true,
    });
    window.dispatchEvent(escape);
    expect(dismiss).not.toHaveBeenCalled();
    expect(escape.defaultPrevented).toBe(true);

    const modK = new KeyboardEvent("keydown", {
      key: "k",
      code: "KeyK",
      metaKey: true,
      bubbles: true,
      cancelable: true,
    });
    window.dispatchEvent(modK);
    expect(search).not.toHaveBeenCalled();
    expect(modK.defaultPrevented).toBe(true);

    expect(
      dispatchKeyboardEvent({
        key: "Escape",
        code: "Escape",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
      }).handled,
    ).toBe(false);

    setShortcutCaptureActive(false);
    detach();
  });

  it("keeps the listener while any holder remains", () => {
    setDispatcherPlatformForTests("macos");
    const spy = vi.fn();
    registerCommandHandler("search.open", spy);
    const first = attachDispatcher();
    const second = attachDispatcher();

    // The remaining holder keeps the listener active.
    first();
    window.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "k",
        code: "KeyK",
        metaKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    expect(spy).toHaveBeenCalledOnce();

    second();
    window.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "k",
        code: "KeyK",
        metaKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    expect(spy).toHaveBeenCalledOnce();
  });
});

describe("handlers that decline", () => {
  it("hands the key back when the handler cannot act", () => {
    setDispatcherPlatformForTests("macos");
    setComposerFocused(true, {});
    const walked = vi.fn(() => false);
    registerCommandHandler("composer.recallPrev", declinable(walked));
    const ta = document.createElement("textarea");

    const result = dispatchKeyboardEvent(
      evt({ key: "ArrowUp", code: "ArrowUp", target: ta }),
    );

    expect(walked).toHaveBeenCalledOnce();
    // A declined handler leaves the key unconsumed.
    expect(result.commandId).toBe(stockId("composer-recall-prev"));
    expect(result.handled).toBe(false);
  });

  it("consumes the key when the same handler does act", () => {
    setDispatcherPlatformForTests("macos");
    setComposerFocused(true, {});
    registerCommandHandler("composer.recallPrev", declinable(() => true));
    const ta = document.createElement("textarea");

    expect(
      dispatchKeyboardEvent(evt({ key: "ArrowUp", code: "ArrowUp", target: ta }))
        .handled,
    ).toBe(true);
  });

  it("leaves a declined key to the page's own keydown handling", () => {
    setDispatcherPlatformForTests("macos");
    setComposerFocused(true, {});
    registerCommandHandler("composer.recallPrev", declinable(() => false));
    const detach = attachDispatcher();
    const ta = document.createElement("textarea");
    document.body.appendChild(ta);

    const event = new KeyboardEvent("keydown", {
      key: "ArrowUp",
      code: "ArrowUp",
      bubbles: true,
      cancelable: true,
    });
    ta.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(false);
    ta.remove();
    detach();
  });
});

describe("host-executed commands on the keyboard", () => {
  /** Builds a frame with one contributed shortcut. */
  function seedExtensionBinding(disabled = false): void {
    seedStockFrame();
    seedContributionFrameForTest({
      ...STOCK_FRAME,
      commands: [
        ...STOCK_FRAME.commands,
        {
          id: "acme/pack:run-audit",
          provider: "acme/pack",
          title: "Run audit",
          scope: "global",
          executor: "host",
          invocation: "session",
          action_kind: "workflow_start",
          icon: "route",
          result_treatment: "receipt",
          ...(disabled ? { enablement: { fact: "editor_has_selection" as const } } : {}),
        },
      ],
      keybindings: [
        ...STOCK_FRAME.keybindings,
        {
          id: "acme/pack:key-run-audit",
          command: "acme/pack:run-audit",
          scope: "global",
          allow_in_input: false,
          bindings: { macos: ["Mod+Shift+U"] },
        },
      ],
      binding_defaults: [
        ...STOCK_FRAME.binding_defaults,
        {
          platform: "macos",
          scope: "global",
          chord: "Mod+Shift+U",
          active: "acme/pack:key-run-audit",
          candidates: ["acme/pack:key-run-audit"],
        },
      ],
    });
  }

  it("runs a bound extension command through the invoke lane", () => {
    setDispatcherPlatformForTests("macos");
    seedExtensionBinding();
    const invoked = vi.fn();
    const release = registerHostCommandInvoker(invoked);

    const result = dispatchKeyboardEvent(
      evt({ key: "u", code: "KeyU", metaKey: true, shiftKey: true }),
    );

    // Contributed commands use the host invoke lane.
    expect(invoked).toHaveBeenCalledWith("acme/pack:run-audit");
    expect(result.handled).toBe(true);
    release();
  });

  it("leaves the key unhandled once the lane is released", () => {
    setDispatcherPlatformForTests("macos");
    seedExtensionBinding();
    const invoked = vi.fn();
    registerHostCommandInvoker(invoked)();

    expect(
      dispatchKeyboardEvent(
        evt({ key: "u", code: "KeyU", metaKey: true, shiftKey: true }),
      ).handled,
    ).toBe(false);
    expect(invoked).not.toHaveBeenCalled();
  });

  it("does not consume a disabled contributed command's chord", () => {
    setDispatcherPlatformForTests("macos");
    seedExtensionBinding(true);
    setShellFactState({
      ...ALL_SHELL_FACTS,
      editor: { ...ALL_SHELL_FACTS.editor!, hasSelection: false },
    });
    const invoked = vi.fn();
    const release = registerHostCommandInvoker(invoked);

    const result = dispatchKeyboardEvent(
      evt({ key: "u", code: "KeyU", metaKey: true, shiftKey: true }),
    );

    expect(result).toEqual({ commandId: null, chord: "Mod+Shift+U", handled: false });
    expect(invoked).not.toHaveBeenCalled();
    release();
  });

  it("never routes a den command through the invoke lane", () => {
    setDispatcherPlatformForTests("macos");
    const invoked = vi.fn();
    const release = registerHostCommandInvoker(invoked);
    // search.open is den-executed with no implementation registered here.
    const result = dispatchKeyboardEvent(
      evt({ key: "k", code: "KeyK", metaKey: true }),
    );

    expect(invoked).not.toHaveBeenCalled();
    expect(result.handled).toBe(false);
    release();
  });
});

describe("two commands on one chord", () => {
  /** Creates a persisted shortcut collision. */
  function collideOnSearchChord(): void {
    setShortcutOverrides({ [stockBindingId("session-new")]: "Mod+K" });
  }

  it("fires neither when both are available in the same stratum", () => {
    setDispatcherPlatformForTests("macos");
    seedStockFrame();
    collideOnSearchChord();
    const search = vi.fn();
    const sessionNew = vi.fn();
    registerCommandHandler("search.open", search);
    registerCommandHandler("session.new", sessionNew);

    const result = dispatchKeyboardEvent(
      evt({ key: "k", code: "KeyK", metaKey: true }),
    );

    expect(result).toEqual({ commandId: null, chord: "Mod+K", handled: false });
    expect(search).not.toHaveBeenCalled();
    expect(sessionNew).not.toHaveBeenCalled();
  });

  it("is not ambiguous when only one of them is available", () => {
    setDispatcherPlatformForTests("macos");
    // session.new is unavailable without a project.
    seedStockFrame({ projectOpen: false });
    collideOnSearchChord();
    const search = vi.fn();
    registerCommandHandler("search.open", search);
    registerCommandHandler("session.new", vi.fn());

    expect(
      dispatchKeyboardEvent(evt({ key: "k", code: "KeyK", metaKey: true })).commandId,
    ).toBe(stockId("search-open"));
    expect(search).toHaveBeenCalledOnce();
  });

  it("still lets a higher stratum win a shared chord", () => {
    setDispatcherPlatformForTests("macos");
    const send = vi.fn();
    const confirm = vi.fn();
    registerCommandHandler("composer.send", send);
    registerCommandHandler("list.confirm", confirm);
    setComposerFocused(true, {});
    const release = claimOverlayScope();

    // The active overlay controls Enter.
    const result = dispatchKeyboardEvent(evt({ key: "Enter", code: "Enter" }));

    expect(result.commandId).toBe(stockId("list-confirm"));
    expect(confirm).toHaveBeenCalledOnce();
    expect(send).not.toHaveBeenCalled();
    release();
  });
});

describe("two declarations for one command on one chord", () => {
  it("fires neither instead of choosing one declaration's policy by frame order", () => {
    setDispatcherPlatformForTests("macos");
    seedContributionFrameForTest({
      ...STOCK_FRAME,
      keybindings: [
        ...STOCK_FRAME.keybindings,
        {
          id: "acme/pack:key-search-alternate",
          command: stockId("search-open"),
          scope: "global",
          allow_in_input: false,
          bindings: { macos: ["Mod+L"] },
        },
      ],
      binding_defaults: [
        ...STOCK_FRAME.binding_defaults,
        {
          platform: "macos",
          scope: "global",
          chord: "Mod+L",
          active: "acme/pack:key-search-alternate",
          candidates: ["acme/pack:key-search-alternate"],
        },
      ],
    });
    setShortcutOverrides({ "acme/pack:key-search-alternate": "Mod+K" });
    const search = vi.fn();
    registerCommandHandler("search.open", search);

    const result = dispatchKeyboardEvent(
      evt({ key: "k", code: "KeyK", metaKey: true }),
    );

    expect(result).toEqual({ commandId: null, chord: "Mod+K", handled: false });
    expect(
      shortcutBindingAvailable(stockBindingId("search-open"), "Mod+K"),
    ).toBe(false);
    expect(
      shortcutCommandMatchesEvent(
        stockId("search-open"),
        evt({ key: "k", code: "KeyK", metaKey: true }),
      ),
    ).toBe(false);
    expect(
      invokeShortcutBinding(stockBindingId("search-open"), "Mod+K"),
    ).toBe(false);
    expect(search).not.toHaveBeenCalled();
  });
});
