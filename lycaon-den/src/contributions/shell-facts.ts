/** Binds the fact vocabulary to shell state; the host re-verifies host facts. */
import type { FactLookup } from "./conditions.ts";
import type { ContributionFactId } from "./contribution-facts.generated.ts";
import { createSignal } from "solid-js";

export type ShellEditorFacts = {
  active: boolean;
  editable: boolean;
  hasSelection: boolean;
  symbol: string | null;
  findingId: string | null;
  language: string | null;
};

export type ShellFactState = {
  activeView: string;
  mountedRegions: readonly string[];
  workspaceKind: string;
  isPeerWorkspace: boolean;
  contextFeatures: readonly string[];
  composerFocused: boolean;
  /** Destination reachability from this workspace, distinct from mounted. */
  chatNavigable: boolean;
  contextNavigable: boolean;
  sidebarExpanded: boolean;
  /** The person hid the conversation beside a split stage. */
  conversationCollapsed: boolean;
  contextCollapsed: boolean;
  /** A stage and the conversation share the window as a split. */
  splitLive: boolean;
  filesStageActive: boolean;
  /** The Files navigator is hidden in this window. */
  filesTreeCollapsed: boolean;
  fileSummariesOn: boolean;
  /** The file-summaries setting has loaded; before that its value is a guess. */
  fileSummariesKnown: boolean;
  /** The active Files editor shows a past version rather than the working file. */
  filesVersionHistorical: boolean;
  peerOpenAvailable: boolean;
  peerViewsPresent: boolean;
  projectOpen: boolean;
  sessionExists: boolean;
  sessionIdle: boolean;
  activityLive: boolean;
  editor: ShellEditorFacts | null;
  requirementReady: (requirementId: string) => boolean;
  /** A declared boolean setting's value in force, from the same frame. */
  configurationOn: (configurationId: string) => boolean;
};

// A Record over the generated id union: a missing binder is a type error.
const FACT_BINDERS: Record<
  ContributionFactId,
  (state: ShellFactState, operand: string) => boolean
> = {
  project_open: (s) => s.projectOpen,
  session_exists: (s) => s.sessionExists,
  session_idle: (s) => s.sessionIdle,
  activity_live: (s) => s.activityLive,
  editor_active: (s) => s.editor?.active ?? false,
  editor_editable: (s) => s.editor?.editable ?? false,
  editor_has_selection: (s) => s.editor?.hasSelection ?? false,
  editor_has_symbol: (s) => (s.editor?.symbol ?? "") !== "",
  editor_has_finding: (s) => (s.editor?.findingId ?? "") !== "",
  editor_language: (s, operand) => s.editor?.language === operand,
  mcp_requirement_ready: (s, operand) => s.requirementReady(operand),
  configuration_on: (s, operand) => s.configurationOn(operand),
  active_view: (s, operand) => s.activeView === operand,
  view_mounted: (s, operand) => s.mountedRegions.includes(operand),
  workspace_kind: (s, operand) => s.workspaceKind === operand,
  peer_workspace: (s) => s.isPeerWorkspace,
  context_feature: (s, operand) => s.contextFeatures.includes(operand),
  composer_focused: (s) => s.composerFocused,
  chat_navigable: (s) => s.chatNavigable,
  context_navigable: (s) => s.contextNavigable,
  sidebar_expanded: (s) => s.sidebarExpanded,
  context_collapsed: (s) => s.contextCollapsed,
  conversation_collapsed: (s) => s.conversationCollapsed,
  split_live: (s) => s.splitLive,
  files_stage_active: (s) => s.filesStageActive,
  files_tree_collapsed: (s) => s.filesTreeCollapsed,
  file_summaries_on: (s) => s.fileSummariesOn,
  file_summaries_known: (s) => s.fileSummariesKnown,
  files_version_historical: (s) => s.filesVersionHistorical,
  peer_open_available: (s) => s.peerOpenAvailable,
  peer_views_present: (s) => s.peerViewsPresent,
};

function shellFactLookup(state: ShellFactState): FactLookup {
  return (fact, operand) => FACT_BINDERS[fact]?.(state, operand) ?? false;
}

/** Live fact state, republished on every shell store change. */
const [liveState, setLiveState] = createSignal<ShellFactState | null>(null);

export function setShellFactState(state: ShellFactState): void {
  setLiveState(state);
}

export function liveShellFactLookup(): FactLookup | null {
  const state = liveState();
  return state ? shellFactLookup(state) : null;
}
