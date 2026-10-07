import { describe, expect, it } from "vitest";
import { denSourceRoot, loadTypeScriptCorpus } from "../test/source-corpus.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

const productionSources = loadTypeScriptCorpus(denSourceRoot, { excludeTests: true }).files;

describe("modal shortcut handling", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  it("keeps overlay focus control on the shared helper", () => {
    const overlayTraps = productionSources
      .filter((file) => file.text.includes("createOverlayScopeFocusTrap("))
      .map((file) => file.rel)
      .sort();

    expect(overlayTraps).toEqual([
      "components/onboarding/OnboardingGate.tsx",
      "components/project/ProjectLauncher.tsx",
      "components/search/Crossbar.tsx",
      "components/shell/PeerViewSwitcher.tsx",
      "components/shortcuts/ShortcutHelpOverlay.tsx",
      "platform/interaction/modal-focus-trap.ts",
    ]);
  });

  it("keeps the low-level focus trap behind the modal focus module", () => {
    const directUsers = productionSources
      .filter((file) => file.rel !== "platform/interaction/focus-trap.ts")
      .filter((file) => file.text.includes("activateFocusTrap"))
      .map((file) => file.rel);

    expect(directUsers).toEqual(["platform/interaction/modal-focus-trap.ts"]);
  });

  it("keeps overlay scope claims behind the lifecycle helper", () => {
    const directUsers = productionSources
      .filter((file) => file.rel !== "shortcuts/dispatcher.ts")
      .filter((file) => file.text.includes("claimOverlayScope("))
      .map((file) => file.rel);

    expect(directUsers).toEqual(["platform/interaction/modal-focus-trap.ts"]);
  });
});
