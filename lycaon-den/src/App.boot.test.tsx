import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import App from "./App.tsx";

vi.mock("./platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./platform/runtime.ts")>()),
  isTauriRuntime: () => false,
  usesCustomWindowChrome: () => false,
  tauriDragRegionProps: () => ({}),
  tauriPlatform: () => null,
  detectedPlatform: () => null,
}));

vi.mock("./platform/windows/window-chrome.ts", () => ({
  tagTauriPlatformClasses: () => undefined,
  setupWindowChrome: async () => undefined,
  windowFocused: () => true,
  setWindowFocused: () => undefined,
  windowGeometryClaimPending: () => false,
  widenWindowBy: async () => "claimed",
  focusAppWindow: async () => undefined,
  revealWindow: async () => undefined,
}));

vi.mock("./platform/connection/app-connection.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./platform/connection/app-connection.ts")>();
  return {
    ...actual,
    connectAppBackend: vi.fn().mockRejectedValue(new Error("offline")),
    disconnectAppBackend: vi.fn(),
  };
});

vi.mock("./settings/system/onboarding-prefs.ts", () => ({
  firstRunSetupCompletedPref: () => true,
  onboardingPrefsReady: () => true,
  syncOnboardingPrefsFromSnapshot: () => undefined,
  completeFirstRunSetup: async () => undefined,
}));

describe("App boot", () => {
  it("renders den-shell UI even when the backend is offline", async () => {
    render(() => <App />);
    expect(await screen.findByTestId("home-view")).toBeTruthy();
    expect(screen.getByTestId("home-nav")).toBeTruthy();
    expect(
      document.querySelector(".den-shell-stage .den-shell-header"),
    ).toBeTruthy();
  });
});
