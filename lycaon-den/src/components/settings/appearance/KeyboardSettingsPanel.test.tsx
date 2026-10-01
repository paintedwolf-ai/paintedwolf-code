import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import {
  KeyboardSettingsPanel,
  matchesShortcutQuery,
} from "./KeyboardSettingsPanel.tsx";
import {
  resetShortcutPrefsForTests,
  shortcutOverrides,
  saveShortcutOverride,
  syncShortcutPrefsFromSnapshot,
} from "../../../settings/system/shortcut-prefs.ts";
import {
  attachDispatcher,
  registerCommandHandler,
  resetDispatcherForTests,
} from "../../../shortcuts/dispatcher.ts";
import { contributionFrame, resetContributionStoreForTest } from "../../../contributions/contribution-store.ts";
import { shortcutDisplayEntries } from "../../../shortcuts/command-display.ts";
import { NAV_LEADER_ID } from "../../../shortcuts/keymap.ts";
import {
  seedStockFrame,
  stockBindingId,
} from "../../../contributions/stock-frame-test.ts";
import { setShortcutPlatformForTests } from "../../../shortcuts/platform.ts";
import {
  setAppStateSnapshot,
} from "../../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../../shared/app-state-types.ts";

vi.mock("../../../store/app-state-snapshot.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../store/app-state-snapshot.ts")>();
  return {
    ...actual,
    loadSharedAppState: vi.fn(async () => actual.getAppStateSnapshot()),
  };
});

function pressKey(init: KeyboardEventInit): KeyboardEvent {
  const event = new KeyboardEvent("keydown", {
    bubbles: true,
    cancelable: true,
    ...init,
  });
  window.dispatchEvent(event);
  return event;
}

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

describe("KeyboardSettingsPanel", () => {
  it("lists commands in scope tabs with current bindings", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-settings-panel")).toBeTruthy(),
    );
    expect(screen.getByTestId("keyboard-tab-global")).toBeTruthy();
    expect(screen.getByTestId("keyboard-tab-files")).toBeTruthy();
    expect(screen.getByTestId("keyboard-tab-composer")).toBeTruthy();
    expect(screen.getByTestId("keyboard-tab-overlay")).toBeTruthy();
    expect(screen.getByTestId("keyboard-scope-global")).toBeTruthy();
    expect(
      screen.getByTestId(`keyboard-chord-${stockBindingId("search-open")}`).getAttribute("data-binding"),
    ).toBe("⌘K");
    expect(screen.queryByTestId(`keyboard-row-${stockBindingId("composer-send")}`)).toBeNull();

    fireEvent.click(screen.getByTestId("keyboard-tab-composer"));
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-row-${stockBindingId("composer-send")}`)).toBeTruthy(),
    );
    expect(screen.getByTestId("keyboard-scope-composer")).toBeTruthy();
  });

  it("reflects an override in the chord display", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(
        screen.getByTestId(`keyboard-chord-${stockBindingId("search-open")}`).getAttribute("data-binding"),
      ).toBe("⌘J"),
    );
  });

  it("records a chord and saves the override", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`));
    pressKey({
      key: "j",
      code: "KeyJ",
      metaKey: true,
    });
    await waitFor(() =>
      expect(shortcutOverrides()[stockBindingId("search-open")]).toBe("Mod+J"),
    );
    expect(
      screen.getByTestId(`keyboard-chord-${stockBindingId("search-open")}`).getAttribute("data-binding"),
    ).toBe("⌘J");
  });

  it("keeps recording open without saving IME composition keys", async () => {
    render(() => <KeyboardSettingsPanel />);
    const recordId = `keyboard-record-${stockBindingId("search-open")}`;
    await waitFor(() => expect(screen.getByTestId(recordId)).toBeTruthy());
    fireEvent.click(screen.getByTestId(recordId));

    pressKey({ key: "Enter", code: "Enter", isComposing: true });
    expect(shortcutOverrides()[stockBindingId("search-open")]).toBeUndefined();
    expect(
      screen.getByTestId(`keyboard-chord-${stockBindingId("search-open")}`).textContent,
    ).toMatch(/Press keys/);

    pressKey({ key: "j", code: "KeyJ", metaKey: true });
    await waitFor(() =>
      expect(shortcutOverrides()[stockBindingId("search-open")]).toBe("Mod+J"),
    );
  });

  it("refuses a reserved chord inline without saving", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`));
    pressKey({
      key: "q",
      code: "KeyQ",
      metaKey: true,
    });
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-reserved-refuse")).toBeTruthy(),
    );
    expect(shortcutOverrides()[stockBindingId("search-open")]).toBeUndefined();
  });

  it("does not run app shortcuts while recording (Escape cancels only)", async () => {
    const dismiss = vi.fn();
    const search = vi.fn();
    registerCommandHandler("overlay.dismiss", dismiss);
    registerCommandHandler("search.open", search);
    const detach = attachDispatcher();

    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`)).toBeTruthy(),
    );

    fireEvent.click(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`));
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-chord-${stockBindingId("search-open")}`).textContent).toMatch(
        /Press keys/,
      ),
    );

    // Escape must cancel capture without dismissing settings (overlay.dismiss).
    pressKey({ key: "Escape", code: "Escape" });
    expect(dismiss).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(
        screen.getByTestId(`keyboard-chord-${stockBindingId("search-open")}`).getAttribute("data-binding"),
      ).toBe("⌘K"),
    );

    // Re-enter capture: Mod+K must not open search.
    fireEvent.click(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`));
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-chord-${stockBindingId("search-open")}`).textContent).toMatch(
        /Press keys/,
      ),
    );
    pressKey({ key: "k", code: "KeyK", metaKey: true });
    expect(search).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(shortcutOverrides()[stockBindingId("search-open")]).toBe("Mod+K"),
    );

    detach();
  });

  it("records a two-step sequence: leader, then a bare key", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${stockBindingId("go-chat")}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-record-${stockBindingId("go-chat")}`));

    // The leader opens a sequence instead of binding as a chord.
    pressKey({ key: ";", code: "Semicolon", metaKey: true });
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-chord-${stockBindingId("go-chat")}`).textContent).toMatch(
        /⌘; then…/,
      ),
    );
    expect(shortcutOverrides()[stockBindingId("go-chat")]).toBeUndefined();

    pressKey({ key: "d", code: "KeyD" });
    await waitFor(() => expect(shortcutOverrides()[stockBindingId("go-chat")]).toBe("Mod+; D"));
    expect(
      screen.getByTestId(`keyboard-chord-${stockBindingId("go-chat")}`).getAttribute("data-binding"),
    ).toBe("⌘; then D");
  });

  it("refuses a modified second key and says why", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${stockBindingId("go-chat")}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-record-${stockBindingId("go-chat")}`));
    pressKey({ key: ";", code: "Semicolon", metaKey: true });
    pressKey({ key: "d", code: "KeyD", altKey: true });

    await waitFor(() =>
      expect(
        screen.getByTestId("keyboard-reserved-refuse").textContent,
      ).toMatch(/second key/i),
    );
    expect(shortcutOverrides()[stockBindingId("go-chat")]).toBeUndefined();
  });

  it("the leader row still records a plain chord", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${NAV_LEADER_ID}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-record-${NAV_LEADER_ID}`));
    pressKey({ key: "'", code: "Quote", metaKey: true });
    await waitFor(() =>
      expect(shortcutOverrides()[NAV_LEADER_ID]).toBe("Mod+'"),
    );
  });

  it("does not let a leader rebind shadow a custom declaration", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+'");
    const result = await saveShortcutOverride(NAV_LEADER_ID, "Mod+'");
    expect(result.ok).toBe(false);
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(
        screen.getByTestId(
          `keyboard-chord-${stockBindingId("search-open")}`,
        ).getAttribute("data-binding"),
      ).toBe("⌘'"),
    );
    expect(shortcutOverrides()[NAV_LEADER_ID]).toBeUndefined();
  });

  it("names the screen-reader reservation separately from the system one", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-record-${stockBindingId("search-open")}`));
    pressKey({ key: "Insert", code: "Insert" });
    await waitFor(() =>
      expect(
        screen.getByTestId("keyboard-reserved-refuse").textContent,
      ).toMatch(/screen readers/i),
    );
    expect(shortcutOverrides()[stockBindingId("search-open")]).toBeUndefined();
  });

  it("sanitizes loaded same-scope conflicts before rendering", async () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      shortcuts: {
        overrides: {
          [stockBindingId("search-open")]: "Mod+K",
          [stockBindingId("launcher-open")]: "Mod+K",
        },
      },
    });
    syncShortcutPrefsFromSnapshot();
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() => expect(shortcutOverrides()).toEqual({}));
    expect(
      screen.queryByTestId(
        `keyboard-conflict-${stockBindingId("search-open")}`,
      ),
    ).toBeNull();
  });

  it("resets one override and reset-all clears the map", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
    await saveShortcutOverride(stockBindingId("session-new"), "Mod+Shift+B");
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-reset-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-reset-${stockBindingId("search-open")}`));
    await waitFor(() =>
      expect(shortcutOverrides()[stockBindingId("search-open")]).toBeUndefined(),
    );
    expect(shortcutOverrides()[stockBindingId("session-new")]).toBe("Mod+Shift+B");

    fireEvent.click(screen.getByTestId("keyboard-reset-all"));
    await waitFor(() => expect(shortcutOverrides()).toEqual({}));
  });

  it("filters shortcuts by label", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-filter")).toBeTruthy(),
    );
    fireEvent.input(screen.getByTestId("keyboard-filter"), {
      target: { value: "search" },
    });
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-row-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
    expect(screen.queryByTestId(`keyboard-row-${stockBindingId("composer-send")}`)).toBeNull();
    expect(screen.queryByTestId("keyboard-scope-composer")).toBeNull();

    fireEvent.input(screen.getByTestId("keyboard-filter"), {
      target: { value: "zzzz-no-match" },
    });
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-filter-empty")).toBeTruthy(),
    );
  });

  it("marks custom bindings with a badge", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-custom-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
  });

  it("filters by chord and by command id, not just label", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-filter")).toBeTruthy(),
    );
    const filter = screen.getByTestId("keyboard-filter");

    // Chord, in the form a user types — the stored chord is `Mod+K`, shown as `⌘K`,
    // and the label ("Open Crossbar") contains neither.
    fireEvent.input(filter, { target: { value: "mod+k" } });
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-row-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
    expect(screen.queryByTestId(`keyboard-row-${stockBindingId("find-in-view")}`)).toBeNull();

    // The rendered glyph form finds it too.
    fireEvent.input(filter, { target: { value: "⌘K" } });
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-row-${stockBindingId("search-open")}`)).toBeTruthy(),
    );

    // Command id.
    fireEvent.input(filter, { target: { value: "search-open" } });
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-row-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
  });

  it("follows an override when filtering by chord", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-filter")).toBeTruthy(),
    );
    fireEvent.input(screen.getByTestId("keyboard-filter"), {
      target: { value: "mod+j" },
    });
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-row-${stockBindingId("search-open")}`)).toBeTruthy(),
    );
  });

  it("every keybinding declaration has one row with rebind and reset controls", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-settings-panel")).toBeTruthy(),
    );
    const scopes = ["global", "files", "composer", "overlay"] as const;
    for (const scope of scopes) {
      fireEvent.click(screen.getByTestId(`keyboard-tab-${scope}`));
      await waitFor(() =>
        expect(screen.getByTestId(`keyboard-panel-${scope}`)).toBeTruthy(),
      );
      for (const entry of shortcutDisplayEntries(contributionFrame()).filter(
        (candidate) => candidate.scope === scope,
      )) {
        expect(
          screen.getByTestId(`keyboard-row-${entry.id}`),
          `row for ${entry.id}`,
        ).toBeTruthy();
        expect(
          screen.getByTestId(`keyboard-record-${entry.id}`),
          `rebind for ${entry.id}`,
        ).toBeTruthy();
        expect(
          screen.getByTestId(`keyboard-reset-${entry.id}`),
          `reset for ${entry.id}`,
        ).toBeTruthy();
      }
    }
  });

  it("shows a default leader row and records a custom leader chord", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-group-leader")).toBeTruthy(),
    );
    expect(screen.getByTestId(`keyboard-row-${NAV_LEADER_ID}`)).toBeTruthy();
    expect(
      screen.getByTestId(`keyboard-chord-${NAV_LEADER_ID}`).getAttribute("data-binding"),
    ).toBe("⌘;");
    expect(screen.queryByTestId(`keyboard-custom-${NAV_LEADER_ID}`)).toBeNull();

    fireEvent.click(screen.getByTestId(`keyboard-record-${NAV_LEADER_ID}`));
    pressKey({
      key: "'",
      code: "Quote",
      metaKey: true,
    });
    await waitFor(() =>
      expect(shortcutOverrides()[NAV_LEADER_ID]).toBe("Mod+'"),
    );
    expect(
      screen.getByTestId(`keyboard-chord-${NAV_LEADER_ID}`).getAttribute("data-binding"),
    ).toBe("⌘'");
  });

  it("refuses a leader that collides with a catalog chord", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${NAV_LEADER_ID}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-record-${NAV_LEADER_ID}`));
    pressKey({
      key: "k",
      code: "KeyK",
      metaKey: true,
    });
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-reserved-refuse")).toBeTruthy(),
    );
    expect(shortcutOverrides()[NAV_LEADER_ID]).toBeUndefined();
  });

  it("refuses a Shift-only leader", async () => {
    render(() => <KeyboardSettingsPanel />);
    await waitFor(() =>
      expect(screen.getByTestId(`keyboard-record-${NAV_LEADER_ID}`)).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId(`keyboard-record-${NAV_LEADER_ID}`));
    pressKey({ key: ";", code: "Semicolon", shiftKey: true });
    await waitFor(() =>
      expect(screen.getByTestId("keyboard-reserved-refuse").textContent).toContain(
        "non-Shift modifier",
      ),
    );
    expect(shortcutOverrides()[NAV_LEADER_ID]).toBeUndefined();
  });
});

describe("matchesShortcutQuery", () => {
  const cmd = { id: "search.open", title: "Open Crossbar" };
  const chords = ["Mod+K"];
  const display = "⌘K";
  const match = (q: string) => matchesShortcutQuery(q, cmd, chords, display);

  it("finds F12, Mod+Shift+L, and a bare command id", () => {
    const def = {
      id: "files.goToDefinition",
      title: "Go to definition",
    };
    expect(matchesShortcutQuery("F12", def, ["F12"], "F12")).toBe(true);
    const split = {
      id: "editor.splitSelectionIntoLines",
      title: "Split selection into lines",
    };
    expect(
      matchesShortcutQuery("Mod+Shift+L", split, ["Mod+Shift+L"], "⌘⇧L"),
    ).toBe(true);
    const join = { id: "editor.joinLines", title: "Join lines" };
    expect(matchesShortcutQuery("joinLines", join, ["Mod+J"], "⌘J")).toBe(true);
    expect(
      matchesShortcutQuery("editor.joinLines", join, ["Mod+J"], "⌘J"),
    ).toBe(true);
  });

  it("keeps everything for an empty or whitespace query", () => {
    expect(match("")).toBe(true);
    expect(match("   ")).toBe(true);
  });

  it("matches label and command id case-insensitively", () => {
    expect(match("CROSSBAR")).toBe(true);
    expect(match("search.open")).toBe(true);
    expect(match("Search.Open")).toBe(true);
  });

  it("matches the chord however the separators are typed", () => {
    for (const q of ["mod+k", "Mod-K", "mod k", "modk", "MOD+K"]) {
      expect(match(q), q).toBe(true);
    }
  });

  it("matches the platform display form", () => {
    expect(match("⌘K")).toBe(true);
    expect(match("⌘")).toBe(true);
  });

  it("rejects a genuine miss", () => {
    expect(match("zzzz-no-match")).toBe(false);
    expect(match("mod+j")).toBe(false);
  });

  it("treats a separator-only query as no filter rather than matching nothing", () => {
    expect(match("+")).toBe(true);
  });
});
