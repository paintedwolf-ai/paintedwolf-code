import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@solidjs/testing-library";

let platform: "macos" | "linux" = "linux";
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  tauriPlatform: () => platform,
}));

const { OnboardingLayoutPreview } = await import("./OnboardingLayoutPreview.tsx");
const { setWindowChromeLayoutForTests } = await import("../../platform/windows/window-chrome-layout.ts");

function titlebar(): HTMLElement {
  const bar = screen.getByTestId("onboarding-layout-preview-chat").querySelector<HTMLElement>(
    ".onboarding-layout-preview__titlebar",
  );
  if (!bar) throw new Error("no preview title bar");
  return bar;
}

const controls = (side: "left" | "right") =>
  [...titlebar().querySelectorAll(`[data-side="${side}"] > i`)].map((i) => i.getAttribute("data-control"));

afterEach(() => {
  cleanup();
  platform = "linux";
  setWindowChromeLayoutForTests(null);
});

describe("OnboardingLayoutPreview title bar", () => {
  it("draws the desktop's buttons on the sides it puts them", () => {
    setWindowChromeLayoutForTests({ left: ["close"], right: ["appmenu", "minimize"], source: "portal" });
    render(() => <OnboardingLayoutPreview kind="chat" mirrored={false} />);
    expect(titlebar().dataset.windowControls).toBe("drawn");
    expect(controls("left")).toEqual(["close"]);
    expect(controls("right")).toEqual(["appmenu", "minimize"]);
  });

  it("keeps the three window buttons on a Mac", () => {
    platform = "macos";
    render(() => <OnboardingLayoutPreview kind="chat" mirrored={false} />);
    expect(titlebar().dataset.windowControls).toBeUndefined();
    expect(titlebar().querySelectorAll(":scope > i")).toHaveLength(3);
  });
});
