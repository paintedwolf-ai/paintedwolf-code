// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { createShellNavigationState } from "./shell-navigation-state.ts";

const disposals: (() => void)[] = [];
afterEach(() => {
  for (const dispose of disposals.splice(0)) dispose();
  document.body.replaceChildren();
});
function navigationState() {
  return createRoot((dispose) => {
    disposals.push(dispose);
    return createShellNavigationState(() => "project");
  });
}

describe("shell navigation and focus restoration", () => {
  it("retains the original return surface and focus through settings-to-context navigation", async () => {
    const navigation = navigationState();
    const trigger = document.createElement("button");
    document.body.append(trigger);
    navigation.setNav("files");
    trigger.focus();
    navigation.setNav("settings");
    navigation.setNav("context");
    const other = document.createElement("button");
    document.body.append(other);
    other.focus();
    expect(document.activeElement).toBe(other);
    navigation.closeSettingsNav();
    expect(navigation.nav()).toBe("files");
    await Promise.resolve();
    await Promise.resolve();
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps layout and settings in the same tray slot", () => {
    const navigation = navigationState();
    navigation.toggleLayoutTray();
    expect(navigation.dockTray()).toBe("layout");
    navigation.setNav("settings");
    expect(navigation.dockTray()).toBe("settings");
    navigation.toggleLayoutTray();
    expect(navigation.nav()).toBe("projects");
    expect(navigation.dockTray()).toBe("layout");
    navigation.toggleLayoutTray();
    expect(navigation.dockTray()).toBeNull();
  });
});
