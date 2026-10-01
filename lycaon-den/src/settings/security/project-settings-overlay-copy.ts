/** Shared copy for project Configuration panels that mirror Settings (AI providers,
 *  Approvals, MCP). One top-level override; disabled (default) follows Settings.
 *  Scope ledes reuse each panel’s Settings intro — only the cross-link differs. */

import {
  AI_PROVIDERS_SECTION_LABEL,
  SECURITY_SCANNERS_SECTION_LABEL,
} from "../settings-nav-model.ts";

export type ProjectSettingsMirrorSection =
  | "providers"
  | "approvals"
  | "mcp"
  | "scanners";

/** Device Settings destination for each project mirror section. */
export type DeviceSettingsCounterpart =
  | "providers"
  | "approvals"
  | "mcp"
  | "scanners";

export const PROJECT_SETTINGS_OVERLAY_COPY = {
  /** Pill next to the panel title. */
  badge: "Project",
  /** Stage title + gear affordance. */
  stageTitle: "Project configuration",
  /** Project configuration path labels. */
  settingsPath: {
    providers: AI_PROVIDERS_SECTION_LABEL,
    approvals: "Approvals",
    mcp: "MCP providers",
    scanners: SECURITY_SCANNERS_SECTION_LABEL,
  } satisfies Record<ProjectSettingsMirrorSection, string>,
  /** Matching device Settings path labels (same names as project mirrors). */
  deviceSettingsPath: {
    providers: AI_PROVIDERS_SECTION_LABEL,
    approvals: "Approvals",
    mcp: "MCP providers",
    scanners: SECURITY_SCANNERS_SECTION_LABEL,
  } satisfies Record<ProjectSettingsMirrorSection, string>,
  /** Project → device Settings CTA. */
  openInSettings: (path: string) => `Settings → ${path}`,
  /** Device Settings → project Configuration CTA. */
  openInProjectSettings: (path: string) => `Project → ${path}`,
  /** Named project variant when a display name is known. */
  openInProjectSettingsNamed: (projectName: string, path: string) =>
    `${projectName} → ${path}`,
  /**
   * One-line cue above the counterpart jump. Settings means every project;
   * Project configuration means only the open project.
   */
  counterpartHintFromSettings: "For this project only",
  counterpartHintFromProject: "Device-wide for every project",
  overrideTitle: (settingsPath: string) => `Override Settings → ${settingsPath}`,
  overrideHint:
    "When off, this project uses your Settings values. Turn on only when this project should differ.",
  overrideToggleLabel: "Override",
  followingSettings: (summary: string) => `Following Settings · ${summary}`,
} as const;

export const MIRROR_TO_DEVICE_SECTION: Record<
  ProjectSettingsMirrorSection,
  DeviceSettingsCounterpart
> = {
  providers: "providers",
  approvals: "approvals",
  mcp: "mcp",
  scanners: "scanners",
};

export const DEVICE_TO_MIRROR_SECTION: Record<
  DeviceSettingsCounterpart,
  ProjectSettingsMirrorSection
> = {
  providers: "providers",
  approvals: "approvals",
  mcp: "mcp",
  scanners: "scanners",
};

export function isProjectSettingsMirrorSection(
  value: string,
): value is ProjectSettingsMirrorSection {
  return (
    value === "providers" ||
    value === "approvals" ||
    value === "mcp" ||
    value === "scanners"
  );
}
