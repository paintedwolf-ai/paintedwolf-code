import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { TypographySettings } from "./TypographySettings.tsx";
import { EMPTY_APP_STATE_V1 } from "../../../../shared/app-state-types.ts";
import { setAppStateSnapshot } from "../../../store/app-state-snapshot.ts";
import {
  resetFontPrefsForTests,
  syncFontPrefsFromSnapshot,
} from "../../../settings/appearance/font-prefs.ts";
import {
  SYSTEM_FONT,
  bundledFontsInRole,
} from "../../../fonts/font-catalog.ts";
import { DEN_FONT_DEFAULTS, DEN_FONT_FALLBACKS } from "../../../fonts/font-catalog.generated.ts";
import { clearBootFontsForTests } from "../../../fonts/boot-font-cache.ts";
import { resetTextScaleForTests } from "../../../platform/desktop/accessibility-text-size.ts";
import {
  resetProductTextScaleForTests,
  syncProductTextScaleFromSnapshot,
} from "../../../settings/appearance/text-scale-prefs.ts";

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

/** Simulate a machine with no custom fonts. */
function stubNothingInstalled(): void {
  const context = { font: "", measureText: () => ({ width: 400 }) };
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
    context as unknown as CanvasRenderingContext2D,
  );
}

function chooseValue(testId: string, value: string): void {
  fireEvent.click(screen.getByTestId(testId));
  const option = screen
    .getAllByRole("option")
    .find((candidate) => candidate.getAttribute("data-value") === value);
  if (!option) throw new Error(`Missing option: ${value}`);
  fireEvent.click(option);
}

beforeEach(() => {
  setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
  resetFontPrefsForTests();
  resetProductTextScaleForTests();
  resetTextScaleForTests();
  clearBootFontsForTests();
});

afterEach(() => {
  vi.restoreAllMocks();
  resetFontPrefsForTests();
  resetProductTextScaleForTests();
  resetTextScaleForTests();
});

describe("TypographySettings", () => {
  it("offers every bundled family for its own role, plus system and custom", () => {
    render(() => <TypographySettings />);
    fireEvent.click(screen.getByTestId("font-select-ui"));
    const uiValues = screen
      .getAllByRole("option")
      .map((option) => option.getAttribute("data-value"));
    for (const font of bundledFontsInRole("ui")) {
      expect(uiValues).toContain(font.family);
    }
    expect(uiValues).toContain(SYSTEM_FONT);
    expect(uiValues).toContain("custom");

    // Interface choices exclude code-only fonts.
    expect(uiValues).not.toContain(DEN_FONT_DEFAULTS.mono);
    fireEvent.keyDown(screen.getByRole("listbox"), { key: "Escape" });
    fireEvent.click(screen.getByTestId("font-select-mono"));
    expect(
      screen
        .getAllByRole("option")
        .map((option) => option.getAttribute("data-value")),
    ).toContain(DEN_FONT_DEFAULTS.mono);
  });

  it("starts on the product defaults", () => {
    render(() => <TypographySettings />);

    expect(screen.getByTestId("font-select-ui").textContent).toContain(
      DEN_FONT_DEFAULTS.ui,
    );
    expect(screen.getByTestId("font-select-mono").textContent).toContain(
      DEN_FONT_DEFAULTS.mono,
    );
  });

  it("persists a device text scale alongside the OS accessibility scale", async () => {
    render(() => <TypographySettings />);

    chooseValue("product-text-scale", "1.15");

    await waitFor(() => {
      expect(document.documentElement.style.getPropertyValue("--den-text-scale")).toBe(
        "1.15",
      );
    });
    expect(screen.getByTestId("product-text-scale").textContent).toContain("115%");
  });

  it("restores the persisted product text scale", () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { textScale: 1.3 },
    });
    syncProductTextScaleFromSnapshot();
    render(() => <TypographySettings />);

    expect(screen.getByTestId("product-text-scale").textContent).toContain("130%");
  });

  it("previews the family it would apply", () => {
    render(() => <TypographySettings />);
    const preview = screen.getByTestId("font-preview-ui");

    expect(preview.getAttribute("style")).toContain(DEN_FONT_DEFAULTS.ui);
  });

  it("applies a bundled selection to the document token", async () => {
    render(() => <TypographySettings />);

    chooseValue("font-select-ui", "Literata");

    await waitFor(() => {
      expect(
        document.documentElement.style.getPropertyValue("--den-font-ui"),
      ).toContain('"Literata"');
    });
  });

  it("resolves the system option to the platform stack", async () => {
    render(() => <TypographySettings />);

    chooseValue("font-select-mono", SYSTEM_FONT);

    await waitFor(() => {
      expect(
        document.documentElement.style.getPropertyValue("--den-font-mono"),
      ).toBe(DEN_FONT_FALLBACKS.mono);
    });
  });

  it("says so when a typed family is not installed", async () => {
    // Custom fonts surface failed availability checks.
    stubNothingInstalled();
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { monoFont: "Berkeley Mono" },
    });
    syncFontPrefsFromSnapshot();

    render(() => <TypographySettings />);

    expect(screen.getByTestId("font-select-mono").textContent).toContain("Custom");
    expect(screen.getByTestId("font-missing-mono").textContent).toContain(
      "Berkeley Mono",
    );
  });

  it("never reports a bundled family as missing", () => {
    // Bundled fonts skip availability probes.
    stubNothingInstalled();
    render(() => <TypographySettings />);

    expect(screen.queryByTestId("font-missing-ui")).toBeNull();
    expect(screen.queryByTestId("font-missing-mono")).toBeNull();
  });

  it("stores nothing until the custom field carries a usable name", async () => {
    render(() => <TypographySettings />);

    chooseValue("font-select-ui", "custom");

    // Empty custom values leave the active font unchanged.
    expect(
      document.documentElement.style.getPropertyValue("--den-font-ui"),
    ).not.toContain("custom");
    expect(screen.getByTestId("font-custom-ui")).toBeTruthy();

    fireEvent.change(screen.getByTestId("font-custom-ui"), {
      target: { value: "Helvetica Neue" },
    });

    await waitFor(() => {
      expect(
        document.documentElement.style.getPropertyValue("--den-font-ui"),
      ).toContain('"Helvetica Neue"');
    });
  });
});
