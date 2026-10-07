import {
  ADVANCED_SETTINGS_TABS,
  APP_SETTINGS,
  GENERAL_SETTINGS_TABS,
  type AdvancedSettingsTab,
  type GeneralSettingsTab,
  type SettingsSection,
} from "./settings-nav-model.ts";
import {
  APPROVALS_SETTINGS_COPY,
  NEVER_ASK_COPY,
  type ApprovalsSettingsTab,
} from "./security/approvals-settings-copy.ts";
import {
  WEB_RESEARCH_SETTINGS_COPY,
  type WebResearchSettingsTab,
} from "./research/web-research-settings-copy.ts";
import { MODELS_SETTINGS_COPY } from "./providers/models-settings-copy.ts";
import { MODELS_ROLE_FILTER_COPY } from "./providers/models-role-filter-copy.ts";
import { MCP_SETTINGS_COPY } from "./mcp/mcp-settings-copy.ts";
import { SCANNERS_SETTINGS_COPY, scannerSlotLabel } from "./extensions/scanners-settings-copy.ts";
import { COST_SETTINGS_COPY } from "./budgets/cost-settings-copy.ts";
import { DATA_SETTINGS_COPY } from "./storage/data-settings-copy.ts";
import { CACHE_SETTINGS_COPY } from "./storage/cache-settings-copy.ts";

/** Sections whose panels switch between tabs of their own. */
export type SettingsSectionTabs = {
  general: GeneralSettingsTab;
  debug: AdvancedSettingsTab;
  approvals: ApprovalsSettingsTab;
  "web-research": WebResearchSettingsTab;
};

export type TabbedSettingsSection = keyof SettingsSectionTabs;

const SECTION_TAB_LABELS: {
  [S in TabbedSettingsSection]: Record<SettingsSectionTabs[S], string>;
} = {
  general: Object.fromEntries(
    GENERAL_SETTINGS_TABS.map((tab) => [tab.id, tab.label]),
  ) as Record<GeneralSettingsTab, string>,
  debug: Object.fromEntries(
    ADVANCED_SETTINGS_TABS.map((tab) => [tab.id, tab.label]),
  ) as Record<AdvancedSettingsTab, string>,
  approvals: {
    ask: APPROVALS_SETTINGS_COPY.tabAsk,
    saved: APPROVALS_SETTINGS_COPY.tabSaved,
    detections: APPROVALS_SETTINGS_COPY.tabDetections,
  },
  "web-research": {
    providers: WEB_RESEARCH_SETTINGS_COPY.tabProviders,
    index: WEB_RESEARCH_SETTINGS_COPY.tabIndex,
  },
};

/** Where a setting's row renders: a section, and the tab when the section has tabs. */
export type SettingLocation =
  | {
      [S in TabbedSettingsSection]: { section: S; tab: SettingsSectionTabs[S] };
    }[TabbedSettingsSection]
  | { section: Exclude<SettingsSection, TabbedSettingsSection>; tab?: undefined };

type SettingSpec = SettingLocation & {
  id: string;
  /** The row's visible label; rows render it from here. */
  label: string;
  /** Titled group the row sits under, when the label needs it for context. */
  group?: string;
  /** Lowercase words people type for this setting; matched, never shown. */
  keywords: readonly string[];
};

const DISPLAY = { section: "general", tab: "display" } as const;
const NOTIFICATIONS = { section: "general", tab: "notifications" } as const;
const EDITOR = { section: "general", tab: "editor" } as const;
const BUDGETS = { section: "debug", tab: "budgets" } as const;
const DIAGNOSTICS = { section: "debug", tab: "diagnostics" } as const;

const SETTING_SPECS = [
  { ...EDITOR, id: "editor-keymap", label: "Editing style", keywords: ["standard", "emacs", "vim", "keymap", "keyboard", "select all", "modal editing"] },
  { ...EDITOR, id: "editor-tab-focus", label: "Tab moves focus", keywords: ["keyboard", "indent", "accessibility", "tab"] },
  // General · Display
  { ...DISPLAY, id: "appearance", label: "Appearance", keywords: ["theme", "dark mode", "light mode", "color scheme", "match system"] },
  { ...DISPLAY, id: "light-theme", label: "Light theme", keywords: ["theme", "colors", "color theme", "palette"] },
  { ...DISPLAY, id: "dark-theme", label: "Dark theme", keywords: ["theme", "dark mode", "colors", "color theme", "palette"] },
  { ...DISPLAY, id: "text-size", label: "Text size", group: "Type", keywords: ["font size", "zoom", "scale", "bigger text", "smaller text", "accessibility"] },
  { ...DISPLAY, id: "interface-font", label: "Interface font", group: "Type", keywords: ["font", "ui font", "typeface", "app font"] },
  { ...DISPLAY, id: "code-font", label: "Code font", group: "Type", keywords: ["font", "monospace", "mono font", "typeface"] },
  { ...DISPLAY, id: "message-times", label: "Message times", keywords: ["timestamps", "time sent", "clock"] },
  { ...DISPLAY, id: "collapse-diffs", label: "Collapse diffs by default", keywords: ["diff preview", "file edits", "expand diffs"] },
  { ...DISPLAY, id: "file-summaries", label: "File summaries", keywords: ["ai summaries", "summarize files"] },
  { ...DISPLAY, id: "diff-word-wrap", label: "Word wrap in diffs", keywords: ["wrap lines", "long lines", "diff"] },
  { ...DISPLAY, id: "show-all-models", label: MODELS_ROLE_FILTER_COPY.showAllModelsLabel, keywords: ["model list", "model picker", "hidden models"] },
  { ...DISPLAY, id: "first-time-tips", label: "Show first-time tips", keywords: ["tips", "hints", "onboarding", "tutorial"] },
  { ...DISPLAY, id: "open-at-launch", label: "Open at launch", group: "Layout", keywords: ["startup", "launch", "split view", "layout"] },
  { ...DISPLAY, id: "narrow-window", label: "Too narrow for both columns", group: "Layout", keywords: ["narrow window", "small window", "split view", "columns"] },
  { ...DISPLAY, id: "split-order", label: "Chat position", group: "Layout", keywords: ["swap", "split", "chat side", "left", "right", "beside sidebar"] },
  { ...DISPLAY, id: "workspace-orientation", label: "Workspace orientation", group: "Layout", keywords: ["mirrored", "flip", "left", "right", "sidebar side"] },

  // General · Notifications
  { ...NOTIFICATIONS, id: "notifications", label: "Enable notifications", keywords: ["notifications", "alerts", "notify", "banners"] },
  { ...NOTIFICATIONS, id: "notify-finished", label: "Finished", group: "Notification kinds", keywords: ["notifications", "run finished", "done", "completed"] },
  { ...NOTIFICATIONS, id: "notify-needs-input", label: "Needs your input", group: "Notification kinds", keywords: ["notifications", "question", "waiting"] },
  { ...NOTIFICATIONS, id: "notify-needs-approval", label: "Needs your approval", group: "Notification kinds", keywords: ["notifications", "approval", "checkpoint"] },
  { ...NOTIFICATIONS, id: "test-notification", label: "Send test notification", keywords: ["notifications", "test"] },

  // General · Editor
  { ...EDITOR, id: "editor-font-family", label: "Font family", group: "Text", keywords: ["editor font", "code font", "typeface", "monospace"] },
  { ...EDITOR, id: "editor-font-size", label: "Font size", group: "Text", keywords: ["editor font size", "text size", "zoom", "bigger text", "smaller text"] },
  { ...EDITOR, id: "editor-line-height", label: "Line height", group: "Text", keywords: ["line spacing", "leading"] },
  { ...EDITOR, id: "reveal-in-tree", label: "Reveal opened files in tree", group: "Text", keywords: ["file tree", "auto reveal", "follow file", "sync tree"] },
  { ...EDITOR, id: "tree-visible-levels", label: "Visible tree levels", group: "Text", keywords: ["file tree", "sticky folders", "pinned rows", "indentation folding"] },
  { ...EDITOR, id: "editor-word-wrap", label: "Word wrap", group: "Text", keywords: ["wrap lines", "soft wrap", "long lines"] },
  { ...EDITOR, id: "line-numbers", label: "Line numbers", group: "Text", keywords: ["gutter"] },
  { ...EDITOR, id: "walk-controls-placement", label: "Default walk controls placement", group: "Version history", keywords: ["walk", "floating", "docked", "history controls"] },
  { ...EDITOR, id: "walk-controls-location", label: "Saved walk controls location", group: "Version history", keywords: ["walk", "reset location", "history controls"] },
  { ...EDITOR, id: "version-comparison", label: "Default comparison", group: "Version history", keywords: ["version history", "diff", "before", "current", "compare"] },
  { ...EDITOR, id: "indent-style", label: "Style", group: "Indentation", keywords: ["indent style", "tabs", "spaces"] },
  { ...EDITOR, id: "indent-width", label: "Width", group: "Indentation", keywords: ["indent width", "tab size", "tab width"] },
  { ...EDITOR, id: "indent-guides", label: "Indentation guides", group: "Guides", keywords: ["indent guides", "vertical guides"] },
  { ...EDITOR, id: "show-whitespace", label: "Show whitespace", group: "Guides", keywords: ["whitespace", "invisible characters", "trailing whitespace"] },
  { ...EDITOR, id: "scrollbar-ticks", label: "Show scrollbar ticks", group: "Scrollbar ticks", keywords: ["scrollbar", "minimap", "overview ruler", "markers"] },
  { ...EDITOR, id: "scrollbar-ticks-symbols", label: "Symbols", group: "Scrollbar ticks", keywords: ["scrollbar", "functions", "classes", "declarations"] },
  { ...EDITOR, id: "scrollbar-ticks-changes", label: "Changes", group: "Scrollbar ticks", keywords: ["scrollbar", "added lines", "removed lines"] },
  { ...EDITOR, id: "scrollbar-ticks-matches", label: "Matches", group: "Scrollbar ticks", keywords: ["scrollbar", "find results", "search"] },
  { ...EDITOR, id: "scrollbar-ticks-cursor", label: "Cursor", group: "Scrollbar ticks", keywords: ["scrollbar", "caret", "selection"] },
  { ...EDITOR, id: "scrollbar-ticks-windows", label: "Window selections", group: "Scrollbar ticks", keywords: ["scrollbar", "other windows"] },
  { ...EDITOR, id: "scrollbar-ticks-agents", label: "Agent activity", group: "Scrollbar ticks", keywords: ["scrollbar", "agents"] },
  { ...EDITOR, id: "agent-activity", label: "Show agent activity in files", group: "Agent activity", keywords: ["agents", "presence", "what agents read", "pending edits"] },
  { ...EDITOR, id: "agent-activity-reads", label: "What agents read", group: "Agent activity", keywords: ["agents", "reads"] },
  { ...EDITOR, id: "agent-activity-changes", label: "Changes about to land", group: "Agent activity", keywords: ["agents", "pending edits", "drafts"] },
  { ...EDITOR, id: "agent-activity-file-names", label: "File names", group: "Agent activity", keywords: ["agents", "file tree", "open files"] },
  { ...EDITOR, id: "external-editor", label: "External editor", group: "Opening files", keywords: ["open in", "vs code", "cursor", "custom open command", "custom folder command"] },
  { ...EDITOR, id: "browser", label: "Browser", group: "Opening links", keywords: ["default browser", "web links", "custom browser command"] },

  // General · Keyboard, Power, Updates, About
  { section: "general", tab: "keyboard", id: "keyboard-shortcuts", label: "Filter shortcuts", keywords: ["keyboard shortcuts", "keybindings", "hotkeys", "rebind", "leader key"] },
  { section: "general", tab: "power", id: "keep-awake", label: "Keep Mac awake while sessions are working", keywords: ["sleep", "power", "idle", "caffeinate"] },
  { section: "general", tab: "updates", id: "update-checks", label: "Automatic updates", keywords: ["updates", "auto update", "check for updates"] },
  { section: "general", tab: "updates", id: "release-channel", label: "Release channel", keywords: ["updates", "preview", "beta", "stable", "release candidate"] },
  { section: "general", tab: "updates", id: "check-for-updates", label: "Check now", keywords: ["check for updates", "update now", "upgrade"] },
  { section: "general", tab: "about", id: "tell-a-friend", label: "Tell a friend", keywords: ["share", "star", "github", "website"] },
  { section: "general", tab: "about", id: "privacy", label: "Privacy", keywords: ["telemetry", "analytics", "tracking", "crash reporting"] },
  { section: "general", tab: "about", id: "third-party-software", label: "Third-party software", keywords: ["licenses", "notices", "open source"] },

  // AI providers
  { section: "providers", id: "ai-providers", label: MODELS_SETTINGS_COPY.addProvider, keywords: ["api key", "token", "credentials", "provider", "connect", "sign in", "openai", "anthropic", "gemini", "ollama"] },
  { section: "providers", id: "default-model", label: "Default model", keywords: ["model", "llm", "coordinator model"] },
  { section: "providers", id: "summarizer-model", label: MODELS_SETTINGS_COPY.defaultSummarizerModelLabel, keywords: ["summarizer model", "summaries", "lite model", "cheap model"] },
  { section: "providers", id: "thinking-overrides", label: "Override thinking", keywords: ["thinking", "reasoning", "extended thinking", "thinking budget"] },

  // Approvals
  { section: "approvals", tab: "ask", id: "approval-level", label: APPROVALS_SETTINGS_COPY.approvalLevelHeading, keywords: ["permissions", "posture", "auto approve", "light", "balanced", "strict"] },
  { section: "approvals", tab: "ask", id: "approval-ai-note", label: APPROVALS_SETTINGS_COPY.aiRationaleLabel, keywords: ["rationale", "explanation", "approval cards"] },
  { section: "approvals", tab: "ask", id: "extension-policies", label: APPROVALS_SETTINGS_COPY.managedRulesHeading, keywords: ["managed rules", "extension rules", "policies"] },

  // MCP providers
  { section: "mcp", id: "mcp-providers", label: MCP_SETTINGS_COPY.addProvider, keywords: ["mcp", "mcp server", "add server", "model context protocol", "tools", "api key", "token"] },

  // Security scanners
  { section: "scanners", id: "security-scanners", label: SCANNERS_SETTINGS_COPY.mainLabel, keywords: ["security", "scan", "enable scanners"] },
  { section: "scanners", id: "scan-after-changes", label: SCANNERS_SETTINGS_COPY.landedChangeLabel, keywords: ["security", "scan scope", "landed changes", "full root"] },
  { section: "scanners", id: "scan-source-reading", label: SCANNERS_SETTINGS_COPY.sourceVerifyLabel, keywords: ["security", "verify source", "content hash"] },
  { section: "scanners", id: "scanner-sast", label: scannerSlotLabel("sast"), group: SCANNERS_SETTINGS_COPY.jobsHeading, keywords: ["sast", "semgrep", "code scanning", "insecure patterns"] },
  { section: "scanners", id: "scanner-sca", label: scannerSlotLabel("sca"), group: SCANNERS_SETTINGS_COPY.jobsHeading, keywords: ["sca", "cve", "vulnerabilities", "dependency scanning"] },
  { section: "scanners", id: "scanner-secret", label: scannerSlotLabel("secret"), group: SCANNERS_SETTINGS_COPY.jobsHeading, keywords: ["secret scanning", "credentials", "leaked keys"] },

  // Web research
  { section: "web-research", tab: "providers", id: "web-research", label: WEB_RESEARCH_SETTINGS_COPY.enableLabel, keywords: ["web search", "internet", "browse", "online"] },
  { section: "web-research", tab: "providers", id: "web-research-providers", label: WEB_RESEARCH_SETTINGS_COPY.addProvider, keywords: ["web search", "search api", "api key", "token", "credentials"] },
  { section: "web-research", tab: "index", id: "web-research-warming", label: WEB_RESEARCH_SETTINGS_COPY.warmingLabel, keywords: ["web research", "prefetch", "background fetch"] },
  { section: "web-research", tab: "index", id: "web-research-index-storage", label: WEB_RESEARCH_SETTINGS_COPY.indexStorageLabel, keywords: ["web research", "clear index", "disk space", "cache"] },

  // Cost
  { section: "cost", id: "cost-tracking", label: COST_SETTINGS_COPY.mainLabel, keywords: ["cost", "pricing", "pricing sources", "spend", "usage", "dollars"] },

  // Advanced · Budgets
  { ...BUDGETS, id: "spend-ceiling", label: "Spend safety ceiling", keywords: ["budget", "spend limit", "cost limit", "max spend", "soft landing"] },
  { ...BUDGETS, id: "spend-ceiling-usd", label: "Spend safety ceiling (USD)", group: "Spend safety ceiling", keywords: ["budget", "spend limit", "dollars"] },
  { ...BUDGETS, id: "spend-warning", label: "Warn at (% of ceiling)", group: "Spend safety ceiling", keywords: ["budget", "spend warning", "percent"] },
  { ...BUDGETS, id: "max-iterations", label: "Max iterations per turn", group: "Prompt loop", keywords: ["iterations", "loop limit", "turn limit"] },
  { ...BUDGETS, id: "overlay-promote-iterations", label: "Overlay promote max iterations", group: "Prompt loop", keywords: ["iterations", "overlay"] },
  { ...BUDGETS, id: "max-tool-result-bytes", label: "Max tool result bytes", group: "Tool results", keywords: ["tool output", "result size", "truncate"] },
  { ...BUDGETS, id: "coordinator-loop", label: "Coordinator loop", keywords: ["auto-prompt", "worker legs", "loop cycles"] },
  { ...BUDGETS, id: "coordinator-loop-cycles", label: "Max coordinator loop cycles", group: "Coordinator loop", keywords: ["loop cycles", "wake cycles"] },
  { ...BUDGETS, id: "worker-budget-default", label: "Default worker ceiling", group: "Worker tool budget", keywords: ["worker tool budget", "tool loops", "tool rounds", "workers"] },
  { ...BUDGETS, id: "worker-budget-min", label: "Minimum the coordinator may request", group: "Worker tool budget", keywords: ["worker tool budget", "minimum"] },
  { ...BUDGETS, id: "worker-budget-max", label: "Maximum the coordinator may request", group: "Worker tool budget", keywords: ["worker tool budget", "maximum"] },
  { ...BUDGETS, id: "llm-turn-timeout", label: "LLM turn timeout", group: "Wall-clock timeouts", keywords: ["timeout", "seconds"] },
  { ...BUDGETS, id: "coordinator-turn-timeout", label: "Coordinator host turn timeout", group: "Wall-clock timeouts", keywords: ["timeout", "seconds"] },
  { ...BUDGETS, id: "coordinator-max-sleep", label: "Coordinator max sleep", group: "Wall-clock timeouts", keywords: ["timeout", "sleep", "seconds"] },
  { ...BUDGETS, id: "await-workers-timeout", label: "Await parent workers timeout", group: "Wall-clock timeouts", keywords: ["timeout", "workers", "seconds"] },

  // Advanced · Approvals, Cache, Data, Diagnostics
  { section: "debug", tab: "approvals", id: "never-ask", label: NEVER_ASK_COPY.heading, keywords: ["never ask", "disable approvals", "auto approve", "skip permissions", "yolo"] },
  { section: "debug", tab: "cache", id: "clear-caches", label: CACHE_SETTINGS_COPY.clearAllLabel, keywords: ["clear cache", "free disk space", "reset caches"] },
  { section: "debug", tab: "data", id: "backup-restore", label: DATA_SETTINGS_COPY.heading, keywords: ["backup", "restore", "export", "import"] },
  { section: "debug", tab: "data", id: "history-storage", label: "History storage", keywords: ["retention", "prune", "delete history", "disk usage", "protected history"] },
  { ...DIAGNOSTICS, id: "verbose-mode", label: "Verbose mode", keywords: ["debug", "rejects", "diagnostics"] },
  { ...DIAGNOSTICS, id: "debug-logging", label: "Full debug logging", keywords: ["logs", "trace", "debug"] },
  { ...DIAGNOSTICS, id: "system-information", label: "System information", keywords: ["version", "bug report", "readiness"] },
  { ...DIAGNOSTICS, id: "command-line-tool", label: "Command-line tool", keywords: ["pw", "cli", "terminal", "shell command", "install pw"] },
  { ...DIAGNOSTICS, id: "diagnostics-bundle", label: "Diagnostics bundle", keywords: ["logs", "export", "zip", "support", "bug report"] },
] as const satisfies readonly SettingSpec[];

export type SettingId = (typeof SETTING_SPECS)[number]["id"];

export type SettingDefinition = SettingSpec & { id: SettingId };

/** Every individually addressable setting, in Settings order. */
export const SETTINGS_REGISTRY: readonly SettingDefinition[] = SETTING_SPECS;

const BY_ID = new Map<string, SettingDefinition>(
  SETTINGS_REGISTRY.map((setting) => [setting.id, setting]),
);

/** Resolves an id from outside the registry, such as a Crossbar target. */
export function findSettingDefinition(id: string): SettingDefinition | undefined {
  return BY_ID.get(id);
}

export function settingDefinition(id: SettingId): SettingDefinition {
  const setting = BY_ID.get(id);
  if (!setting) throw new Error(`Unknown setting ${id}`);
  return setting;
}

export function settingLabel(id: SettingId): string {
  return settingDefinition(id).label;
}

export function settingsSectionLabel(section: SettingsSection): string {
  return APP_SETTINGS.find((item) => item.id === section)?.label ?? section;
}

/** A tab's label, or undefined when the section has no such tab. */
export function settingsTabLabel(
  section: SettingsSection,
  tab: string,
): string | undefined {
  const labels: Partial<Record<SettingsSection, Record<string, string>>> =
    SECTION_TAB_LABELS;
  const tabs = labels[section];
  return tabs && Object.hasOwn(tabs, tab) ? tabs[tab] : undefined;
}

/** Section, tab, and group, as Crossbar shows them: "General · Editor · Text". */
export function settingContext(setting: SettingDefinition): string {
  const parts = [settingsSectionLabel(setting.section)];
  const tab =
    setting.tab === undefined ? undefined : settingsTabLabel(setting.section, setting.tab);
  if (tab) parts.push(tab);
  if (setting.group) parts.push(setting.group);
  return parts.join(" · ");
}

export const SETTING_ANCHOR_ATTRIBUTE = "data-setting-id";

/** Marks the element a reveal flashes; rows spread it on their outer element. */
export function settingAnchor(id: SettingId): { "data-setting-id": SettingId } {
  return { "data-setting-id": id };
}

export const SETTING_CONTROL_ATTRIBUTE = "data-setting-control";

/** Names the control a reveal focuses when it is not the row's first. */
export function settingControl(): { "data-setting-control": "" } {
  return { "data-setting-control": "" };
}
