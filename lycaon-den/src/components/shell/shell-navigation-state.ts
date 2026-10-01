import { createEffect, createSignal, on, onCleanup } from "solid-js";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import { focusWithoutScroll } from "../../platform/interaction/focus.ts";
import { DEFAULT_PROJECT_CONTEXT_SECTION, DEFAULT_SETTINGS_SECTION, type AdvancedSettingsTab, type GeneralSettingsTab, type ProjectContextSection, type SettingsSection } from "../../settings/settings-nav-model.ts";
import { navDockTray, type NavDockTray } from "../../shell/nav-dock-tray.ts";
import { routedReturnFor, type RoutedReturn } from "../../shell/routed-return.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import { createPresentationIntent } from "../../ui/presentation.ts";
import { isPresented } from "../../ui/presented.ts";

// Full-stage navigation has one foreground value.
export type ShellNav =
  | "projects"
  | "settings"
  | "context"
  | "chats";
export type Nav = ShellNav | ContextNavItemId;

/** A one-shot tab request for the Settings section being opened. */
export type SettingsTabRequest = { general?: GeneralSettingsTab; advanced?: AdvancedSettingsTab };

export type ShellNavigation = ReturnType<typeof createShellNavigationState>;

export function createShellNavigationState(
  activeProjectId: () => string | null,
  revealContext: () => void = () => {},
) {
  const [nav, setNavForeground] = createSignal<Nav>("projects");
  const navigationIntent = createPresentationIntent();
  onCleanup(navigationIntent.dispose);
  createEffect(on(activeProjectId, navigationIntent.cancel));
  let settingsReturnFocus: HTMLElement | null = null;
  let settingsReturnNav: Nav = "projects";
  /** Layout and Settings share one tray slot. */
  const [layoutTrayRaised, setLayoutTrayRaised] = createSignal(false);
  const dockTray = (): NavDockTray =>
    navDockTray({
      settingsForeground: nav() === "settings",
      layoutRaised: layoutTrayRaised(),
    });

  const setNav = (next: Nav) => {
    navigationIntent.cancel();
    if (next !== "projects") revealContext();
    if ((next === "settings" || next === "context") && nav() !== "settings" && nav() !== "context") {
      settingsReturnNav = nav();
      settingsReturnFocus = document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    }
    if (next === "settings") setLayoutTrayRaised(false);
    return setNavForeground(next);
  };

  // Routed visits retain a return target until navigation leaves the stage.
  const [routedReturn, setRoutedReturn] = createSignal<RoutedReturn<Nav> | null>(null);
  // Opening the stage can clear the previous return target.
  const routeToStage = (stage: Nav, open: () => void) => {
    const from = nav();
    open();
    const record = routedReturnFor(from, stage);
    if (record) setRoutedReturn(record);
  };
  createEffect(() => {
    const record = routedReturn();
    if (record && nav() !== record.stage) setRoutedReturn(null);
  });

  const [settingsSection, setSettingsSection] = createSignal<SettingsSection>(DEFAULT_SETTINGS_SECTION);
  const [generalInitialTab, setGeneralInitialTab] = createSignal<GeneralSettingsTab | undefined>();
  const [advancedInitialTab, setAdvancedInitialTab] = createSignal<AdvancedSettingsTab | undefined>();
  const [contextSection, setContextSection] = createSignal<ProjectContextSection>(DEFAULT_PROJECT_CONTEXT_SECTION);
  /** Tab requests apply to one Settings visit. */
  const selectSettingsSection = (section: SettingsSection, tab: SettingsTabRequest = {}) => {
    setGeneralInitialTab(tab.general);
    setAdvancedInitialTab(tab.advanced);
    setSettingsSection(section);
  };
  const openSettings = (section: SettingsSection = DEFAULT_SETTINGS_SECTION, tab?: SettingsTabRequest) => {
    selectSettingsSection(section, tab);
    setNav("settings");
  };
  const openContextSection = (section: ProjectContextSection) => {
    setContextSection(section);
    setNav("context");
  };

  const openLayoutTray = () => {
    if (nav() === "settings") setNav("projects");
    setLayoutTrayRaised(true);
  };
  const closeLayoutTray = () => setLayoutTrayRaised(false);
  const toggleLayoutTray = () => {
    if (dockTray() === "layout") closeLayoutTray();
    else openLayoutTray();
  };
  // Sidebar navigation and Escape leave full-stage surfaces.
  const isSettingsNav = () =>
    nav() === "settings" ||
    nav() === "context" ||
    nav() === "files" ||
    nav() === "security" ||
    nav() === "cost" ||
    nav() === "artifacts" ||
    nav() === "blueprints" ||
    nav() === "extensions";

  const closeSettingsNav = () => {
    const returnFocus = settingsReturnFocus;
    settingsReturnFocus = null;
    const returnNav = nav() === "settings" || nav() === "context" ? settingsReturnNav : "projects";
    settingsReturnNav = "projects";
    setNav(returnNav);
    queueMicrotask(() => {
      queueMicrotask(() => {
        if (returnFocus?.isConnected && isPresented(returnFocus)) {
          focusWithoutScroll(returnFocus);
          if (document.activeElement === returnFocus) return;
        }
        if (!focusRegion("composer")) focusRegion("sidebar");
      });
    });
  };

  return {
    nav, setNav, navigationIntent, dockTray, closeLayoutTray,
    toggleLayoutTray, isSettingsNav, closeSettingsNav, routedReturn, routeToStage,
    settingsSection, generalInitialTab, advancedInitialTab, contextSection,
    selectSettingsSection, openSettings, openContextSection,
  };
}
