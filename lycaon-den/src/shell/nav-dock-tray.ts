/** Settings and Layout share one tray above the dock buttons. */
export type NavDockTray = "settings" | "layout" | null;

/** The Settings stage keeps its section navigation in the tray. */
export function navDockTray(input: {
  settingsForeground: boolean;
  layoutRaised: boolean;
}): NavDockTray {
  if (input.settingsForeground) return "settings";
  return input.layoutRaised ? "layout" : null;
}
