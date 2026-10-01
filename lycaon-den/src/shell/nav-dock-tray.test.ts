import { describe, expect, it } from "vitest";
import { navDockTray } from "./nav-dock-tray.ts";

describe("navDockTray", () => {
  it("is empty until a tray is asked for", () => {
    expect(
      navDockTray({ settingsForeground: false, layoutRaised: false }),
    ).toBeNull();
  });

  it("gives the slot to whichever tray was asked for", () => {
    expect(navDockTray({ settingsForeground: true, layoutRaised: false })).toBe(
      "settings",
    );
    expect(navDockTray({ settingsForeground: false, layoutRaised: true })).toBe(
      "layout",
    );
  });

  it("never answers with both — Settings holds the slot while its stage is up", () => {
    expect(navDockTray({ settingsForeground: true, layoutRaised: true })).toBe(
      "settings",
    );
  });
});
