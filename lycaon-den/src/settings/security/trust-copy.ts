import type { TrustSurfaceId, ExtensionDiagnostic } from "../../api/types.ts";

export const TRUST_COPY = {
  chipLabel: (files: number) => files > 0 ? `Trust ${files}` : "Trust",
  reviewCount: (files: number) => `${files} ${files === 1 ? "file" : "files"} changed`,
  reviewStatus: (unread: number) => unread > 0 ? `${unread} unread ${unread === 1 ? "change" : "changes"}.` : "No unread changes.",
  chipLoading: "Checking trust…",
  chipUnavailable: "Trust unavailable",
  popoverLoading: "Checking project configuration changes.",

  panelTitle: "Trust",
  panelLede:
    "What this project supplies to the agent. Every row starts on and can be switched off. Approvals, sandbox and network rules always come from your settings — a project can never widen them.",
  groupSteering: "Steers the agent",
  groupReplacesDefaults: "Replaces app defaults",
  groupSuggestions: "Suggests device installs",
  panelEmptyTitle: "This project doesn't configure the agent",
  panelEmptyBody:
    "Its folders contain no agent configuration or extension suggestions.",
  offRow: "off for this project",
  deviceOffRow: "off on this device",
  deviceNoteBefore: "Device-wide offs live in ",
  deviceNotePath: "Settings → Advanced → Project trust",
  deviceNoteAfter: ".",
  suggestionsNoteBefore: "Installed packs are managed in ",
  suggestionsNotePath: "Extensions",
  suggestionsNoteAfter: ".",
  listCount: (n: number) => `${n} project configuration ${n === 1 ? "surface" : "surfaces"}`,
  detailBack: "All project configuration",
  detailItems: "Configured items",
  detailAgentsFiles: "AGENTS.md files",
  detailOpenFile: "Open in Files",
  detailLines: (n: number) => `${n} line${n === 1 ? "" : "s"}`,

  sectionTitle: "Project trust",
  sectionIntroBefore:
    "Device-wide switches for project content. Defaults are on. Project choices live under ",
  sectionIntroPath: "Project configuration → Trust",
  sectionIntroAfter: ".",
  loadError: "Could not load project trust.",
  saveError: "Could not save project trust.",

  hints: {
    agents_md:
      "Loaded as instructions. Edits apply on the next turn.",
    skills:
      "Loaded when relevant and followed as instructions. Declared tool lists do not grant access.",
    project_settings:
      "Only tighter than your device settings. Allow rules and saved grants are dropped on load.",
    project_mcp:
      "Enables or disables providers already on this device. Cannot add one, reach a remote, or supply credentials.",
    scan_config:
      "Project scanner settings, finding ignores, and exact public values declared in the ignore file.",
    prompt_overrides:
      "This project supplies its own copies of the app's prompt templates.",
    extension_config:
      "Controls which installed extension units this project disables.",
    extension_suggestions:
      "Shown when opening the project. Nothing is added to this folder.",
  } satisfies Record<TrustSurfaceId, string>,

  countLabel: {
    agents_md: (n: number) => `${n} file${n === 1 ? "" : "s"}`,
    skills: (n: number) => `${n}`,
    project_settings: (n: number) => `${n} file${n === 1 ? "" : "s"}`,
    project_mcp: (n: number) => `${n}`,
    scan_config: (n: number) => `${n} file${n === 1 ? "" : "s"}`,
    prompt_overrides: (n: number) => `${n} file${n === 1 ? "" : "s"}`,
    extension_config: (n: number) => `${n} unit${n === 1 ? "" : "s"}`,
    extension_suggestions: (n: number) => `${n} pack${n === 1 ? "" : "s"}`,
  } satisfies Record<TrustSurfaceId, (n: number) => string>,
} as const;

export function trustSurfaceHint(id: TrustSurfaceId): string {
  return TRUST_COPY.hints[id];
}

export function trustCountLabel(id: TrustSurfaceId, count: number): string {
  return TRUST_COPY.countLabel[id](count);
}

const PROJECT_EXTENSION_REFUSAL_CODES: readonly string[] = ["project_scope_refused"];

export function isProjectExtensionRefusal(code: string): boolean {
  return PROJECT_EXTENSION_REFUSAL_CODES.includes(code);
}

export function projectExtensionRefusalDiagnostics(
  diags: readonly ExtensionDiagnostic[],
): ExtensionDiagnostic[] {
  return diags.filter((d) => isProjectExtensionRefusal(d.code));
}
