import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import axe from "axe-core";
import { createSignal } from "solid-js";
import { ShortcutHelpOverlay } from "./ShortcutHelpOverlay.tsx";
import { contributionFrame, resetContributionStoreForTest } from "../../contributions/contribution-store.ts";
import { shortcutDisplayEntries } from "../../shortcuts/command-display.ts";
import {
  seedStockFrame,
  stockBindingId,
  stockId,
} from "../../contributions/stock-frame-test.ts";
import {
  attachDispatcher,
  dispatchKeyboardEvent,
  registerCommandHandler,
  resetDispatcherForTests,
  setShortcutOverrides,
} from "../../shortcuts/dispatcher.ts";
import { setShortcutPlatformForTests } from "../../shortcuts/platform.ts";
import {
  resetShortcutPrefsForTests,
  saveShortcutOverride,
  shortcutOverrides,
} from "../../settings/system/shortcut-prefs.ts";
import { setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";

vi.mock("../../store/app-state-snapshot.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../store/app-state-snapshot.ts")>();
  return {
    ...actual,
    loadSharedAppState: vi.fn(async () => actual.getAppStateSnapshot()),
    persistAppState: vi.fn(async (patch) => {
      actual.setAppStateSnapshot({
        ...actual.getAppStateSnapshot(),
        ...patch,
      });
    }),
  };
});

beforeEach(() => {
  localStorage.clear();
  setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
  resetDispatcherForTests();
  resetShortcutPrefsForTests();
  setShortcutPlatformForTests("macos");
  seedStockFrame();
});

afterEach(() => {
  resetDispatcherForTests();
  resetShortcutPrefsForTests();
  setShortcutPlatformForTests(null);
  resetContributionStoreForTest();
});

function chordEvent(
  partial: Partial<{
    key: string;
    code: string;
    metaKey: boolean;
    ctrlKey: boolean;
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

describe("ShortcutHelpOverlay", () => {
  it("lists every command under scope or display-group sections", () => {
    render(() => (
      <ShortcutHelpOverlay open onClose={() => {}} />
    ));

    expect(screen.getByTestId("shortcut-help-overlay")).toBeTruthy();
    expect(screen.getByTestId("shortcut-help-scope-global")).toBeTruthy();
    expect(screen.getByTestId("shortcut-help-scope-composer")).toBeTruthy();
    expect(screen.getByTestId("shortcut-help-scope-overlay")).toBeTruthy();
    expect(screen.getByTestId("shortcut-help-scope-files")).toBeTruthy();
    expect(screen.getByTestId("shortcut-help-group-files-editing")).toBeTruthy();
    expect(screen.getByTestId("shortcut-help-group-files-buffers")).toBeTruthy();
    // A group recurring across scopes renders beneath each scope's heading.
    for (const scope of ["global", "files"]) {
      const group = screen.getByTestId(`shortcut-help-group-${scope}-navigation`);
      expect(
        screen.getByTestId(`shortcut-help-scope-${scope}`).contains(group),
      ).toBe(true);
    }

    for (const entry of shortcutDisplayEntries(contributionFrame())) {
      expect(screen.getByTestId(`shortcut-help-row-${entry.id}`)).toBeTruthy();
    }

    expect(
      screen.getByTestId(`shortcut-help-chord-${stockBindingId("search-open")}`).getAttribute("data-binding"),
    ).toBe("⌘K");
    expect(
      screen.getByTestId(`shortcut-help-chord-${stockBindingId("help-shortcuts")}`).getAttribute("data-binding"),
    ).toBe("? / ⌘/");
    expect(
      screen
        .getByTestId(`shortcut-help-chord-${stockBindingId("editor-split-selection-into-lines")}`)
        .getAttribute("data-binding"),
    ).toBe("⌘⇧L");
  });

  it("announces the shape of a binding, not just its keycaps", () => {
    render(() => <ShortcutHelpOverlay open onClose={() => {}} />);

    // Sequence steps and alternatives need audible separators.
    expect(
      screen.getByTestId(`shortcut-help-chord-${stockBindingId("go-chat")}`).textContent,
    ).toContain("then");
    expect(
      screen.getByTestId(`shortcut-help-chord-${stockBindingId("help-shortcuts")}`).textContent,
    ).toContain("or");
  });

  it("axe is green on the shortcuts help overlay", async () => {
    const { container, unmount } = render(() => (
      <ShortcutHelpOverlay open onClose={() => {}} />
    ));
    const results = await axe.run(container, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] },
    });
    const summary = results.violations
      .map(
        (v) =>
          `${v.id}: ${v.help} — ${v.nodes.map((n) => n.target.join(" ")).join("; ")}`,
      )
      .join("\n");
    expect(results.violations, summary || "axe violations").toEqual([]);
    unmount();
  });

  it("reflects a shortcut-prefs override", async () => {
    render(() => (
      <ShortcutHelpOverlay open onClose={() => {}} />
    ));

    await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
    await waitFor(() =>
      expect(shortcutOverrides()[stockBindingId("search-open")]).toBe("Mod+J"),
    );
    await waitFor(() =>
      expect(
        screen.getByTestId(`shortcut-help-chord-${stockBindingId("search-open")}`).getAttribute("data-binding"),
      ).toBe("⌘J"),
    );
  });

  it("? opens via help.shortcuts and Escape closes via overlay.dismiss", () => {
    const [open, setOpen] = createSignal(false);
    const onClose = vi.fn(() => setOpen(false));

    render(() => (
      <ShortcutHelpOverlay open={open()} onClose={onClose} />
    ));

    registerCommandHandler("help.shortcuts", () => setOpen(true));
    registerCommandHandler("overlay.dismiss", () => {
      if (open()) onClose();
    });
    const detach = attachDispatcher();

    expect(screen.queryByTestId("shortcut-help-overlay")).toBeNull();

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "?", code: "Slash", shiftKey: true }),
      ).commandId,
    ).toBe(stockId("help-shortcuts"));
    expect(screen.getByTestId("shortcut-help-overlay")).toBeTruthy();

    expect(
      dispatchKeyboardEvent(chordEvent({ key: "Escape", code: "Escape" }))
        .commandId,
    ).toBe(stockId("overlay-dismiss"));
    expect(onClose).toHaveBeenCalled();
    expect(screen.queryByTestId("shortcut-help-overlay")).toBeNull();

    detach();
  });

  it("Mod+/ also opens help.shortcuts", () => {
    const open = vi.fn();
    registerCommandHandler("help.shortcuts", open);
    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "/", code: "Slash", metaKey: true }),
      ).commandId,
    ).toBe(stockId("help-shortcuts"));
    expect(open).toHaveBeenCalledOnce();
  });

  it("? is suppressed while a text input is focused", () => {
    const open = vi.fn();
    registerCommandHandler("help.shortcuts", open);
    const input = document.createElement("textarea");
    document.body.appendChild(input);

    expect(
      dispatchKeyboardEvent(
        chordEvent({
          key: "?",
          code: "Slash",
          shiftKey: true,
          target: input,
        }),
      ).handled,
    ).toBe(false);
    expect(open).not.toHaveBeenCalled();
    input.remove();
  });

  it("settings affordance invokes onOpenKeyboardSettings", () => {
    const onOpen = vi.fn();
    render(() => (
      <ShortcutHelpOverlay
        open
        onClose={() => {}}
        onOpenKeyboardSettings={onOpen}
      />
    ));
    fireEvent.click(screen.getByTestId("shortcut-help-open-settings"));
    expect(onOpen).toHaveBeenCalledOnce();
  });
});

describe("help.shortcuts overrides in dispatcher", () => {
  it("setShortcutOverrides updates match path used by help chords", () => {
    setShortcutOverrides({ [stockBindingId("help-shortcuts")]: "Mod+J" });
    const open = vi.fn();
    registerCommandHandler("help.shortcuts", open);

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "?", code: "Slash", shiftKey: true }),
      ).handled,
    ).toBe(false);

    expect(
      dispatchKeyboardEvent(
        chordEvent({ key: "j", code: "KeyJ", metaKey: true }),
      ).commandId,
    ).toBe(stockId("help-shortcuts"));
    expect(open).toHaveBeenCalledOnce();
  });
});
