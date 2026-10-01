import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { AppearanceSettings } from "./AppearanceSettings.tsx";
import {
  resetContributionStoreForTest,
  seedContributionFrameForTest,
} from "../../../contributions/contribution-store.ts";
import { STOCK_FRAME } from "../../../contributions/stock-frame.generated.ts";
import {
  appearanceSelection,
  resetAppearancePrefsForTests,
  syncAppearanceFromSnapshot,
} from "../../../settings/appearance/appearance-prefs.ts";
import { setAppStateSnapshot } from "../../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../../shared/app-state-types.ts";

vi.mock("../../../store/app-state-snapshot.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../../store/app-state-snapshot.ts")>();
  return {
    ...actual,
    persistAppState: vi.fn(async (patch) => {
      actual.setAppStateSnapshot({ ...actual.getAppStateSnapshot(), ...patch });
    }),
  };
});

beforeEach(() => {
  setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
  resetAppearancePrefsForTests();
  seedContributionFrameForTest(STOCK_FRAME);
});

afterEach(() => {
  resetContributionStoreForTest();
  resetAppearancePrefsForTests();
});

function chooseValue(testId: string, value: string): void {
  fireEvent.click(screen.getByTestId(testId));
  const option = screen
    .getAllByRole("option")
    .find((candidate) => candidate.getAttribute("data-value") === value);
  if (!option) throw new Error(`Missing option: ${value}`);
  fireEvent.click(option);
}

describe("AppearanceSettings", () => {
  it("offers every stock theme under the appearance it declares", () => {
    render(() => <AppearanceSettings />);
    fireEvent.click(screen.getByTestId("appearance-theme-light"));
    const lightValues = screen
      .getAllByRole("option")
      .map((option) => option.getAttribute("data-value"));
    expect(lightValues).toContain(
      "painted-wolf/platform:daylight",
    );
    expect(lightValues).not.toContain("painted-wolf/platform:charcoal");
    fireEvent.keyDown(screen.getByRole("listbox"), { key: "Escape" });
    fireEvent.click(screen.getByTestId("appearance-theme-dark"));
    const darkValues = screen
      .getAllByRole("option")
      .map((option) => option.getAttribute("data-value"));
    expect(darkValues).toContain(
      "painted-wolf/platform:charcoal",
    );
  });

  it("persists a mode change", async () => {
    render(() => <AppearanceSettings />);
    chooseValue("appearance-mode", "dark");
    await waitFor(() => expect(appearanceSelection().mode).toBe("dark"));
    resetAppearancePrefsForTests();
    syncAppearanceFromSnapshot();
    expect(appearanceSelection().mode).toBe("dark");
  });

  it("says the stock palette is in use before a frame hydrates", () => {
    resetContributionStoreForTest();
    render(() => <AppearanceSettings />);
    expect(screen.getByTestId("appearance-unhydrated")).toBeTruthy();
    expect(screen.queryByTestId("appearance-theme-light")).toBeNull();
  });

  it("keeps theme choices on the device frame", () => {
    render(() => <AppearanceSettings />);

    expect(screen.queryByTestId("appearance-unhydrated")).toBeNull();
    expect(screen.getByTestId("appearance-theme-light")).toBeTruthy();
    expect(screen.getByTestId("appearance-theme-dark")).toBeTruthy();
  });

  it("offers themes with no project scoped — appearance is device config", () => {
    seedContributionFrameForTest(STOCK_FRAME);
    render(() => <AppearanceSettings />);
    expect(screen.queryByTestId("appearance-unhydrated")).toBeNull();
    fireEvent.click(screen.getByTestId("appearance-theme-light"));
    expect(screen.getAllByRole("option").length).toBeGreaterThan(0);
  });

  it("does not nest Type inside the appearance group", () => {
    render(() => <AppearanceSettings />);
    expect(screen.queryByTestId("typography-settings")).toBeNull();
  });

  it("sizes its controls for a settings row, not a dialog", () => {
    // Row controls size to their content.
    render(() => <AppearanceSettings />);
    for (const id of [
      "appearance-mode",
      "appearance-theme-light",
      "appearance-theme-dark",
    ]) {
      expect(
        screen.getByTestId(id).closest(".den-select")?.className,
        `${id} must use the settings-row control`,
      ).toContain("den-settings-select");
    }
  });

  it("names a selected theme that is no longer available", async () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: {
        mode: "dark",
        lightTheme: "painted-wolf/platform:daylight",
        darkTheme: "acme/gone:nightshade",
      },
    });
    syncAppearanceFromSnapshot();
    render(() => <AppearanceSettings />);
    const notice = await screen.findByTestId("appearance-fallback");
    expect(notice.textContent).toContain("acme/gone:nightshade");
    // Missing choices remain selected for later availability.
    expect(appearanceSelection().darkTheme).toBe("acme/gone:nightshade");
  });
});
