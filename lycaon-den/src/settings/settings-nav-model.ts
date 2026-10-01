export type SettingsSection =
  | "general"
  | "providers"
  | "approvals"
  | "edit-review"
  | "tests"
  | "mcp"
  | "scanners"
  | "web-research"
  | "extensions"
  | "cost"
  | "debug";

/** Project configuration tabs. Trust is project-only. */
export type ProjectContextSection =
  | "trust"
  | "secrets"
  | Extract<
      SettingsSection,
      | "providers"
      | "approvals"
      | "mcp"
      | "scanners"
      | "edit-review"
      | "tests"
    >;

/** Tabs inside Settings → General (device-local app prefs). */
export type GeneralSettingsTab =
  | "display"
  | "notifications"
  | "editor"
  | "keyboard"
  | "power"
  | "updates"
  | "about";

type NavTab<T extends string> = { id: T; label: string };

/** Non-empty navigation list. */
type NavList<T> = readonly [T, ...T[]];

export const GENERAL_SETTINGS_TABS: NavList<NavTab<GeneralSettingsTab>> = [
  { id: "display", label: "Display" },
  { id: "notifications", label: "Notifications" },
  { id: "editor", label: "Editor" },
  { id: "keyboard", label: "Keyboard" },
  { id: "power", label: "Power" },
  { id: "updates", label: "Updates" },
  { id: "about", label: "About" },
];

export const DEFAULT_GENERAL_SETTINGS_TAB: GeneralSettingsTab =
  GENERAL_SETTINGS_TABS[0].id;

/** Tabs inside Settings → Advanced (device-global power tools). */
export type AdvancedSettingsTab =
  | "approvals"
  | "budgets"
  | "host_resources"
  | "cache"
  | "data"
  | "diagnostics"
  | "project_trust";

export const ADVANCED_SETTINGS_TABS: NavList<NavTab<AdvancedSettingsTab>> = [
  { id: "budgets", label: "Budgets" },
  { id: "approvals", label: "Approvals" },
  { id: "host_resources", label: "Host resources" },
  { id: "cache", label: "Cache" },
  { id: "data", label: "Data" },
  { id: "diagnostics", label: "Diagnostics" },
  { id: "project_trust", label: "Project trust" },
];

export const DEFAULT_ADVANCED_SETTINGS_TAB: AdvancedSettingsTab =
  ADVANCED_SETTINGS_TABS[0].id;

export type SettingsNavItem<T extends string = SettingsSection> = {
  id: T;
  label: string;
};

/** Shared Settings / Project configuration label for AI providers + model policy. */
export const AI_PROVIDERS_SECTION_LABEL = "AI providers";

/** Shared Settings / Context / project-mirror label for the security scanners product. */
export const SECURITY_SCANNERS_SECTION_LABEL = "Security scanners";

export const APP_SETTINGS: NavList<SettingsNavItem> = [
  { id: "general", label: "General" },
  { id: "providers", label: AI_PROVIDERS_SECTION_LABEL },
  { id: "approvals", label: "Approvals" },
  { id: "mcp", label: "MCP providers" },
  { id: "scanners", label: SECURITY_SCANNERS_SECTION_LABEL },
  { id: "web-research", label: "Web research" },
  { id: "extensions", label: "Extensions" },
  { id: "cost", label: "Cost" },
  { id: "debug", label: "Advanced" },
];

export const PROJECT_CONTEXT: NavList<SettingsNavItem<ProjectContextSection>> = [
  { id: "trust", label: "Trust" },
  { id: "secrets", label: "Secrets" },
  { id: "providers", label: AI_PROVIDERS_SECTION_LABEL },
  { id: "approvals", label: "Approvals" },
  { id: "mcp", label: "MCP providers" },
  { id: "scanners", label: SECURITY_SCANNERS_SECTION_LABEL },
  { id: "edit-review", label: "Edit review" },
  { id: "tests", label: "Tests" },
];

// Each section opens on its first row.
export const DEFAULT_SETTINGS_SECTION: SettingsSection = APP_SETTINGS[0].id;

export const DEFAULT_PROJECT_CONTEXT_SECTION: ProjectContextSection =
  PROJECT_CONTEXT[0].id;

export function isProjectContextSection(
  section: string,
): section is ProjectContextSection {
  return PROJECT_CONTEXT.some((item) => item.id === section);
}
