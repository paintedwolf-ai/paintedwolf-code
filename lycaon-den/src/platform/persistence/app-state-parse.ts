import { parseFilesTreeIntentState } from "../../files/tree/files-tree-intent-state.ts";
import { parseComposerAttachmentDrafts } from "../../chat/composer/composer-attachment-persistence.ts";
import {
  DEFAULT_SESSION_TITLE,
  DEN_FILES_TREE_LEVELS,
  APP_STATE_VERSION,
  EMPTY_APP_STATE_V1,
  RECENTS_CAP,
  CACHED_PROJECTS_CAP,
  TRANSCRIPT_ROW_HEIGHTS_SESSION_CAP,
  COMPOSER_DRAFTS_SESSION_CAP,
  CONTEXT_NAV_CATALOG,
  CHAT_LIST_SORTS,
  type CachedProject,
  type ContextNavItemId,
  type DenAppStateV1,
  type DenDebugPrefs,
  type DenChatPrefs,
  type DenChatListPrefs,
  type DenContextNavPrefs,
  type DenDisplayPrefs,
  type DenEditorPrefs,
  type DenEditorAgentActivityKinds,
  type DenEditorScrollbarTickKinds,
  type DenExternalOpenPrefs,
  type DenFirstTimeTipsPrefs,
  type DenNotificationPrefs,
  type DenOnboardingPrefs,
  type DenWhatsNewPrefs,
  type DenAppearancePrefs,
  isProductTextScale,
  type DenShortcutPrefs,
  type DenLayoutPrefs,
  type DenListPanePrefs,
  type DenListPanesPrefs,
  type RecentSession,
  type NarrowSplitSurvivor,
  type StagePlacement,
} from "../../../shared/app-state-types.ts";
import { SYSTEM_FONT, sanitizeFontFamily } from "../../fonts/font-catalog.ts";
import { isFirstTimeTipId } from "../../first-time-tips/first-time-tips-catalog.ts";
import { parseTranscriptRowHeightsSessions } from "../../chat/transcript/layout/transcript-row-heights-persist.ts";
import { parseTranscriptViewportState } from "../../chat/stream/transcript-viewport-state.ts";
import { parseFilesHotExitState } from "../../files/documents/files-hot-exit.ts";
import { parseFilesTreeViewState } from "../../files/tree/files-tree-view-state.ts";
import { parseReviewLensState } from "../../files/review/review-model.ts";
import { parseClosedBufferRing } from "../../files/tabs/closed-buffer-ring.ts";
import { parseEditorViewStateStore } from "../../components/source/editor/editor-view-state.ts";
import { normalizeContextNavVisible } from "../../settings/editor/context-nav-prefs.ts";
import {
  isBrowserPreset,
  isExternalEditorPreset,
  normalizeExternalOpenPrefs,
} from "../../settings/editor/external-open-prefs.ts";
import { clampListPanePrefs } from "../../list/list-pane-model.ts";
import { clampNavWidthPx } from "../../shell/shell-layout-model.ts";
import { normalizeChatWidthPx } from "../../shell/stage-placement.ts";
import { normalizeBinding } from "../../shortcuts/chord.ts";

type AppStateDoc = Record<string, unknown>;

function parseRecent(value: unknown): RecentSession | null {
  if (typeof value !== "object" || value === null) return null;
  const row = value as Partial<RecentSession>;
  if (typeof row.projectId !== "string" || typeof row.sessionId !== "string") {
    return null;
  }
  const title =
    typeof row.title === "string" && row.title.trim()
      ? row.title.trim()
      : DEFAULT_SESSION_TITLE;
  return {
    projectId: row.projectId,
    sessionId: row.sessionId,
    title,
    ...(typeof row.lastActivityAt === "number" ? { lastActivityAt: row.lastActivityAt } : {}),
  };
}

function normalizeRecents(recents: readonly RecentSession[]): RecentSession[] {
  return recents.map((row) => ({
    ...row,
    title: row.title.trim() || DEFAULT_SESSION_TITLE,
  }));
}

function parseDebug(value: unknown): DenDebugPrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as Partial<DenDebugPrefs>;
  const debug: DenDebugPrefs = {};
  if (typeof row.verboseMode === "boolean") {
    debug.verboseMode = row.verboseMode;
  }
  if (typeof row.fullDebugLogging === "boolean") {
    debug.fullDebugLogging = row.fullDebugLogging;
  }
  return Object.keys(debug).length > 0 ? debug : undefined;
}

function parseDisplay(value: unknown): DenDisplayPrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as Partial<DenDisplayPrefs>;
  const display: DenDisplayPrefs = {};
  if (typeof row.diffWordWrap === "boolean") {
    display.diffWordWrap = row.diffWordWrap;
  }
  if (typeof row.diffCollapsed === "boolean") {
    display.diffCollapsed = row.diffCollapsed;
  }
  if (typeof row.diffSplit === "boolean") {
    display.diffSplit = row.diffSplit;
  }
  if (typeof row.showAllModels === "boolean") {
    display.showAllModels = row.showAllModels;
  }
  return Object.keys(display).length > 0 ? display : undefined;
}

function parseNotifications(value: unknown): DenNotificationPrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as Partial<DenNotificationPrefs>;
  const notifications: DenNotificationPrefs = {};
  if (typeof row.enabled === "boolean") {
    notifications.enabled = row.enabled;
  }
  if (typeof row.finished === "boolean") {
    notifications.finished = row.finished;
  }
  if (typeof row.needsYou === "boolean") {
    notifications.needsYou = row.needsYou;
  }
  if (typeof row.needsApproval === "boolean") {
    notifications.needsApproval = row.needsApproval;
  }
  return Object.keys(notifications).length > 0 ? notifications : undefined;
}

function parseEditor(value: unknown): DenEditorPrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as Record<string, unknown>;
  const editor: DenEditorPrefs = {};
  if (row.keymap === "emacs" || row.keymap === "vim") editor.keymap = row.keymap;
  if (typeof row.tabMovesFocus === "boolean") editor.tabMovesFocus = row.tabMovesFocus;
  if (typeof row.treeVisibleLevels === "number" && Number.isInteger(row.treeVisibleLevels) &&
      (row.treeVisibleLevels === 0 || row.treeVisibleLevels >= DEN_FILES_TREE_LEVELS.min && row.treeVisibleLevels <= DEN_FILES_TREE_LEVELS.max)) {
    editor.treeVisibleLevels = row.treeVisibleLevels;
  }
  if (typeof row.revealInTree === "boolean") editor.revealInTree = row.revealInTree;
  if (typeof row.wordWrap === "boolean") editor.wordWrap = row.wordWrap;
  if (typeof row.lineNumbers === "boolean") editor.lineNumbers = row.lineNumbers;
  if (
    typeof row.fontSize === "number" &&
    Number.isInteger(row.fontSize) &&
    row.fontSize >= 10 &&
    row.fontSize <= 20
  ) {
    editor.fontSize = row.fontSize;
  }
  if (typeof row.indentGuides === "boolean") editor.indentGuides = row.indentGuides;
  if (typeof row.whitespace === "boolean") editor.whitespace = row.whitespace;
  if (row.indentStyle === "spaces" || row.indentStyle === "tabs") {
    editor.indentStyle = row.indentStyle;
  }
  if (row.indentWidth === 2 || row.indentWidth === 4 || row.indentWidth === 8) {
    editor.indentWidth = row.indentWidth;
  }
  if (typeof row.fontFamily === "string" && row.fontFamily.trim()) {
    editor.fontFamily = row.fontFamily.trim();
  }
  if (
    typeof row.lineHeight === "number" &&
    Number.isFinite(row.lineHeight) &&
    row.lineHeight >= 1.1 &&
    row.lineHeight <= 2.0
  ) {
    // Snap to 0.05 steps.
    editor.lineHeight = Math.round(row.lineHeight * 20) / 20;
  }
  if (row.versionComparison === "current" || row.versionComparison === "before") {
    editor.versionComparison = row.versionComparison;
  }
  if (row.walkControlsDefault === "floating" || row.walkControlsDefault === "docked") {
    editor.walkControlsDefault = row.walkControlsDefault;
  }
  const location = row.walkControlsLocation;
  if (location && typeof location === "object" && !Array.isArray(location)) {
    const saved = location as Record<string, unknown>;
    if (saved.placement === "docked") editor.walkControlsLocation = { placement: "docked" };
    if (saved.placement === "floating") {
      if (saved.position === undefined) editor.walkControlsLocation = { placement: "floating" };
      else if (saved.position && typeof saved.position === "object" && !Array.isArray(saved.position)) {
        const { x, y } = saved.position as Record<string, unknown>;
        if (typeof x === "number" && Number.isFinite(x) && x >= 0
          && typeof y === "number" && Number.isFinite(y) && y >= 0) {
          editor.walkControlsLocation = { placement: "floating", position: { x, y } };
        }
      }
    }
  }
  if (typeof row.scrollbarTicks === "boolean") {
    editor.scrollbarTicks = row.scrollbarTicks;
  }
  const tickKinds = parseScrollbarTickKinds(row.scrollbarTickKinds);
  if (tickKinds) editor.scrollbarTickKinds = tickKinds;
  if (typeof row.agentActivity === "boolean") {
    editor.agentActivity = row.agentActivity;
  }
  const activityKinds = parseAgentActivityKinds(row.agentActivityKinds);
  if (activityKinds) editor.agentActivityKinds = activityKinds;
  return Object.keys(editor).length > 0 ? editor : undefined;
}

function parseScrollbarTickKinds(
  value: unknown,
): DenEditorScrollbarTickKinds | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as Record<string, unknown>;
  const kinds: DenEditorScrollbarTickKinds = {};
  if (typeof row.symbols === "boolean") kinds.symbols = row.symbols;
  if (typeof row.changes === "boolean") kinds.changes = row.changes;
  if (typeof row.matches === "boolean") kinds.matches = row.matches;
  if (typeof row.cursor === "boolean") kinds.cursor = row.cursor;
  if (typeof row.windows === "boolean") kinds.windows = row.windows;
  if (typeof row.agents === "boolean") kinds.agents = row.agents;
  return Object.keys(kinds).length > 0 ? kinds : undefined;
}

function parseAgentActivityKinds(
  value: unknown,
): DenEditorAgentActivityKinds | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as Record<string, unknown>;
  const kinds: DenEditorAgentActivityKinds = {};
  if (typeof row.reads === "boolean") kinds.reads = row.reads;
  if (typeof row.changes === "boolean") kinds.changes = row.changes;
  if (typeof row.fileNames === "boolean") kinds.fileNames = row.fileNames;
  return Object.keys(kinds).length > 0 ? kinds : undefined;
}

function parseContextNav(value: unknown): DenContextNavPrefs | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as { visible?: unknown };
  if (!("visible" in row)) return undefined;
  if (!Array.isArray(row.visible)) return undefined;
  return {
    visible: normalizeContextNavVisible(
      row.visible.filter((id): id is string => typeof id === "string"),
    ),
  };
}

function parseOnboarding(value: unknown): DenOnboardingPrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as Partial<DenOnboardingPrefs>;
  const onboarding: DenOnboardingPrefs = {};
  if (typeof row.firstRunSetupCompleted === "boolean") {
    onboarding.firstRunSetupCompleted = row.firstRunSetupCompleted;
  }
  return Object.keys(onboarding).length > 0 ? onboarding : undefined;
}

function parseWhatsNew(value: unknown): DenWhatsNewPrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as Partial<DenWhatsNewPrefs>;
  const whatsNew: DenWhatsNewPrefs = {};
  if (typeof row.lastSeenVersion === "string" && row.lastSeenVersion.trim()) {
    whatsNew.lastSeenVersion = row.lastSeenVersion.trim();
  }
  return Object.keys(whatsNew).length > 0 ? whatsNew : undefined;
}

function parseFirstTimeTips(value: unknown): DenFirstTimeTipsPrefs | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as Partial<DenFirstTimeTipsPrefs>;
  const firstTimeTips: DenFirstTimeTipsPrefs = {};
  if (typeof row.enabled === "boolean") firstTimeTips.enabled = row.enabled;
  if (Array.isArray(row.dismissed)) {
    const dismissed = [...new Set(row.dismissed.filter(isFirstTimeTipId))];
    if (dismissed.length > 0) firstTimeTips.dismissed = dismissed;
  }
  return Object.keys(firstTimeTips).length > 0 ? firstTimeTips : undefined;
}

/** Preserve stored theme IDs until the installed catalog can resolve them. */
export function parseAppearance(value: unknown): DenAppearancePrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as Record<string, unknown>;
  const out: DenAppearancePrefs = {};
  if (row.mode === "light" || row.mode === "dark" || row.mode === "system") {
    out.mode = row.mode;
  }
  if (typeof row.lightTheme === "string" && row.lightTheme.trim()) {
    out.lightTheme = row.lightTheme.trim();
  }
  if (typeof row.darkTheme === "string" && row.darkTheme.trim()) {
    out.darkTheme = row.darkTheme.trim();
  }
  const uiFont = parseFontSelection(row.uiFont);
  if (uiFont) out.uiFont = uiFont;
  const monoFont = parseFontSelection(row.monoFont);
  if (monoFont) out.monoFont = monoFont;
  if (isProductTextScale(row.textScale)) out.textScale = row.textScale;
  return Object.keys(out).length > 0 ? out : undefined;
}

/** Validates CSS-safe font names while preserving unavailable families. */
function parseFontSelection(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  if (value === SYSTEM_FONT) return SYSTEM_FONT;
  return sanitizeFontFamily(value) ?? undefined;
}

export function parseShortcuts(value: unknown): DenShortcutPrefs | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as { overrides?: unknown };
  if (
    typeof row.overrides !== "object" ||
    row.overrides === null ||
    Array.isArray(row.overrides)
  ) {
    return undefined;
  }
  const overrides: Record<string, string> = {};
  for (const [rawId, rawChord] of Object.entries(
    row.overrides as Record<string, unknown>,
  )) {
    // Ids are validated at resolve time against the live frame inventory.
    if (!rawId || typeof rawChord !== "string") continue;
    const chord = normalizeBinding(rawChord);
    if (!chord) continue;
    overrides[rawId] = chord;
  }
  return Object.keys(overrides).length > 0 ? { overrides } : undefined;
}

function parseChat(value: unknown): DenChatPrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as Partial<DenChatPrefs>;
  const chat: DenChatPrefs = {};
  if (row.messageTimes === "hover" || row.messageTimes === "always") {
    chat.messageTimes = row.messageTimes;
  }
  return Object.keys(chat).length > 0 ? chat : undefined;
}

function parseChatList(value: unknown): DenChatListPrefs | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const sort = (value as { sort?: unknown }).sort;
  return CHAT_LIST_SORTS.some((known) => known === sort)
    ? { sort: sort as DenChatListPrefs["sort"] }
    : undefined;
}

export function parseExternalOpen(
  value: unknown,
): DenExternalOpenPrefs | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as Record<string, unknown>;
  const prefs: DenExternalOpenPrefs = {};
  if (
    typeof row.externalEditor === "string" &&
    isExternalEditorPreset(row.externalEditor)
  ) {
    prefs.externalEditor = row.externalEditor;
  }
  if (typeof row.browser === "string" && isBrowserPreset(row.browser)) {
    prefs.browser = row.browser;
  }
  if (typeof row.customOpenCommand === "string") {
    prefs.customOpenCommand = row.customOpenCommand;
  }
  if (typeof row.customOpenFolderCommand === "string") {
    prefs.customOpenFolderCommand = row.customOpenFolderCommand;
  }
  if (typeof row.customBrowserCommand === "string") {
    prefs.customBrowserCommand = row.customBrowserCommand;
  }
  return normalizeExternalOpenPrefs(prefs);
}

const CONTEXT_NAV_ID_SET = new Set<string>(CONTEXT_NAV_CATALOG);

function isStagePlacement(value: string): value is StagePlacement {
  return value === "inline" || value === "split";
}

function isNarrowSplitSurvivor(value: unknown): value is NarrowSplitSurvivor {
  return value === "conversation" || value === "stage";
}

export function parseLayout(
  value: unknown,
): DenLayoutPrefs | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as Record<string, unknown>;
  const prefs: DenLayoutPrefs = {};
  if (typeof row.navWidthPx === "number") {
    prefs.navWidthPx = clampNavWidthPx(row.navWidthPx);
  }
  if (typeof row.navCollapsed === "boolean") {
    prefs.navCollapsed = row.navCollapsed;
  }
  if (row.hiddenSplitPane === "conversation" || row.hiddenSplitPane === "stage") {
    prefs.hiddenSplitPane = row.hiddenSplitPane;
  }
  const listPanes = parseListPanes(row.listPanes);
  if (listPanes) prefs.listPanes = listPanes;
  if (
    row.workspaceOrientation === "standard" ||
    row.workspaceOrientation === "mirrored"
  ) {
    prefs.workspaceOrientation = row.workspaceOrientation;
  }
  if (row.splitOrder === "context-first" || row.splitOrder === "chat-first") {
    prefs.splitOrder = row.splitOrder;
  }
  if (typeof row.chatWidthPx === "number") {
    const chatWidthPx = normalizeChatWidthPx(row.chatWidthPx);
    if (chatWidthPx !== undefined) prefs.chatWidthPx = chatWidthPx;
  }
  if (typeof row.mode === "string") {
    if (isStagePlacement(row.mode)) prefs.mode = row.mode;
  }
  if (
    typeof row.companion === "string" &&
    CONTEXT_NAV_ID_SET.has(row.companion)
  ) {
    prefs.companion = row.companion as ContextNavItemId;
  }
  if (
    typeof row.startupCompanion === "string" &&
    CONTEXT_NAV_ID_SET.has(row.startupCompanion)
  ) {
    prefs.startupCompanion = row.startupCompanion as ContextNavItemId;
  }
  if (isNarrowSplitSurvivor(row.narrowSurvivor)) {
    prefs.narrowSurvivor = row.narrowSurvivor;
  }
  return Object.keys(prefs).length > 0 ? prefs : undefined;
}

/** Dedupes a persisted list of project ids, dropping blanks and non-strings. */
function parseProjectIdList(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const seen = new Set<string>();
  for (const raw of value) {
    if (typeof raw !== "string") continue;
    const id = raw.trim();
    if (id) seen.add(id);
  }
  return seen.size > 0 ? [...seen] : undefined;
}

function parseComposerDrafts(
  value: unknown,
): Record<string, string> | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const next: Record<string, string> = {};
  for (const [sessionId, text] of Object.entries(
    value as Record<string, unknown>,
  )) {
    const id = sessionId.trim();
    if (!id || typeof text !== "string" || text.length === 0) continue;
    next[id] = text;
  }
  const keys = Object.keys(next);
  if (keys.length === 0) return undefined;
  if (keys.length <= COMPOSER_DRAFTS_SESSION_CAP) return next;
  const kept = keys.slice(keys.length - COMPOSER_DRAFTS_SESSION_CAP);
  const capped: Record<string, string> = {};
  for (const key of kept) capped[key] = next[key]!;
  return capped;
}

function parseListPanes(value: unknown): DenListPanesPrefs | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const out: DenListPanesPrefs = {};
  for (const [surface, entry] of Object.entries(value as Record<string, unknown>)) {
    const id = surface.trim();
    if (!id || typeof entry !== "object" || entry === null) continue;
    const pane = clampListPanePrefs(id, entry as DenListPanePrefs);
    if (Object.keys(pane).length > 0) out[id] = pane;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function parseCachedProject(value: unknown): CachedProject | null {
  if (typeof value !== "object" || value === null) return null;
  const row = value as Partial<CachedProject>;
  if (typeof row.id !== "string" || !row.id.trim()) return null;
  if (!Array.isArray(row.roots)) return null;
  const roots = row.roots.flatMap((root) => {
    if (typeof root !== "object" || root === null) return [];
    if (
      typeof root.id !== "string" ||
      typeof root.path !== "string" ||
      typeof root.label !== "string" ||
      !root.label.trim()
    ) return [];
    if (typeof root.is_primary !== "boolean" || typeof root.added_at !== "string") {
      return [];
    }
    if (root.kind !== "draft" && root.kind !== "attached") return [];
    return [
      {
        id: root.id,
        path: root.path,
        label: root.label,
        is_primary: root.is_primary,
        added_at: root.added_at,
        kind: root.kind,
      },
    ];
  });
  if (roots.length !== row.roots.length) return null;
  if (typeof row.roots_generation !== "number") return null;
  if (typeof row.session_count !== "number") return null;
  if (typeof row.starred !== "boolean") return null;
  if (typeof row.is_draft !== "boolean") return null;
  const primaryCount = roots.filter((root) => root.is_primary).length;
  if (roots.length > 0 && primaryCount !== 1) return null;
  if (
    row.is_draft
      ? roots.length !== 1 || roots[0]?.kind !== "draft"
      : roots.some((root) => root.kind === "draft")
  ) {
    return null;
  }
  if (row.promotion !== null && typeof row.promotion !== "object") return null;
  if (typeof row.last_opened_at !== "string") return null;
  if (typeof row.created_at !== "string") return null;
  return {
    id: row.id,
    name: typeof row.name === "string" ? row.name : row.name ?? null,
    roots,
    roots_generation: row.roots_generation,
    session_count: row.session_count,
    starred: row.starred,
    is_draft: row.is_draft,
    promotion:
      row.promotion &&
      typeof row.promotion.destination_path === "string" &&
      typeof row.promotion.init_git === "boolean" &&
      ["queued", "staged", "installed", "committed"].includes(
        row.promotion.phase,
      ) &&
      typeof row.promotion.last_error === "string" &&
      typeof row.promotion.updated_at === "string"
        ? row.promotion
        : null,
    last_activity_at:
      typeof row.last_activity_at === "string" ? row.last_activity_at : null,
    last_opened_at: row.last_opened_at,
    created_at: row.created_at,
  };
}

function parseCachedProjects(value: unknown): CachedProject[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const projects = value.flatMap((row) => {
    const parsed = parseCachedProject(row);
    return parsed ? [parsed] : [];
  });
  return projects.length > 0
    ? projects.slice(0, CACHED_PROJECTS_CAP)
    : undefined;
}

function capState(state: DenAppStateV1): DenAppStateV1 {
  return {
    version: APP_STATE_VERSION,
    recents: state.recents.slice(0, RECENTS_CAP),
    lastActiveProjectId: state.lastActiveProjectId,
    cachedProjects: state.cachedProjects?.slice(0, CACHED_PROJECTS_CAP),
    lastSessionSnapshot: state.lastSessionSnapshot,
    transcriptRowHeights: state.transcriptRowHeights?.slice(
      0,
      TRANSCRIPT_ROW_HEIGHTS_SESSION_CAP,
    ),
    debug: state.debug,
    layout: state.layout,
    display: state.display,
    notifications: state.notifications,
    contextNav: state.contextNav,
    onboarding: state.onboarding,
    whatsNew: state.whatsNew,
    firstTimeTips: state.firstTimeTips,
    appearance: state.appearance,
    shortcuts: state.shortcuts,
    chat: state.chat,
    chatList: state.chatList,
    externalOpen: state.externalOpen,
    editor: state.editor,
    composerDrafts: state.composerDrafts,
    composerAttachments: state.composerAttachments,
    filesHotExit: state.filesHotExit,
    draftPromoteDismissed: state.draftPromoteDismissed,
    nofolderDismissed: state.nofolderDismissed,
    editorViewState: state.editorViewState,
    closedBufferRing: state.closedBufferRing,
    filesTreeView: state.filesTreeView,
    filesTreeIntent: state.filesTreeIntent,
    reviewLens: state.reviewLens,
    transcriptViewport: state.transcriptViewport,
  };
}

/** Parse a document already at APP_STATE_VERSION into typed state. */
function parseCurrentAppState(doc: AppStateDoc): DenAppStateV1 {
  const recents = normalizeRecents(
    Array.isArray(doc.recents)
      ? doc.recents.flatMap((row) => {
          const parsed = parseRecent(row);
          return parsed ? [parsed] : [];
        })
      : [],
  );

  const debug = parseDebug(doc.debug);
  const layout = parseLayout(doc.layout);
  const display = parseDisplay(doc.display);
  const notifications = parseNotifications(doc.notifications);
  const contextNav = parseContextNav(doc.contextNav);
  const onboarding = parseOnboarding(doc.onboarding);
  const whatsNew = parseWhatsNew(doc.whatsNew);
  const firstTimeTips = parseFirstTimeTips(doc.firstTimeTips);
  const appearance = parseAppearance(doc.appearance);
  const shortcuts = parseShortcuts(doc.shortcuts);
  const chat = parseChat(doc.chat);
  const chatList = parseChatList(doc.chatList);
  const externalOpen = parseExternalOpen(doc.externalOpen);
  const editor = parseEditor(doc.editor);
  const composerDrafts = parseComposerDrafts(doc.composerDrafts);
  const filesHotExit = parseFilesHotExitState(doc.filesHotExit);
  const draftPromoteDismissed = parseProjectIdList(doc.draftPromoteDismissed);
  const nofolderDismissed = parseProjectIdList(doc.nofolderDismissed);
  const editorViewState = parseEditorViewStateStore(doc.editorViewState);
  const closedBufferRing = parseClosedBufferRing(doc.closedBufferRing);
  const filesTreeView = parseFilesTreeViewState(doc.filesTreeView);
  const filesTreeIntent = parseFilesTreeIntentState(doc.filesTreeIntent);
  const reviewLens = parseReviewLensState(doc.reviewLens);
  const transcriptViewport = parseTranscriptViewportState(
    doc.transcriptViewport,
  );
  const cachedProjects = parseCachedProjects(doc.cachedProjects);
  const transcriptRowHeights = parseTranscriptRowHeightsSessions(
    doc.transcriptRowHeights,
  );
  const lastActiveProjectId =
    typeof doc.lastActiveProjectId === "string" && doc.lastActiveProjectId.trim()
      ? doc.lastActiveProjectId
      : undefined;

  return capState({
    version: APP_STATE_VERSION,
    recents,
    ...(lastActiveProjectId !== undefined ? { lastActiveProjectId } : {}),
    ...(cachedProjects !== undefined ? { cachedProjects } : {}),
    ...(doc.lastSessionSnapshot !== undefined
      ? { lastSessionSnapshot: doc.lastSessionSnapshot }
      : {}),
    ...(transcriptRowHeights.length > 0 ? { transcriptRowHeights } : {}),
    ...(debug !== undefined ? { debug } : {}),
    ...(layout !== undefined ? { layout } : {}),
    ...(display !== undefined ? { display } : {}),
    ...(notifications !== undefined ? { notifications } : {}),
    ...(contextNav !== undefined ? { contextNav } : {}),
    ...(onboarding !== undefined ? { onboarding } : {}),
    ...(whatsNew !== undefined ? { whatsNew } : {}),
    ...(firstTimeTips !== undefined ? { firstTimeTips } : {}),
    ...(appearance !== undefined ? { appearance } : {}),
    ...(shortcuts !== undefined ? { shortcuts } : {}),
    ...(chat !== undefined ? { chat } : {}),
    ...(chatList !== undefined ? { chatList } : {}),
    ...(externalOpen !== undefined ? { externalOpen } : {}),
    ...(editor !== undefined ? { editor } : {}),
    ...(composerDrafts !== undefined ? { composerDrafts } : {}),
    ...(doc.composerAttachments !== undefined ? { composerAttachments: parseComposerAttachmentDrafts(doc.composerAttachments) } : {}),
    ...(filesHotExit !== undefined ? { filesHotExit } : {}),
    ...(draftPromoteDismissed !== undefined ? { draftPromoteDismissed } : {}),
    ...(nofolderDismissed !== undefined ? { nofolderDismissed } : {}),
    ...(editorViewState !== undefined ? { editorViewState } : {}),
    ...(closedBufferRing !== undefined ? { closedBufferRing } : {}),
    ...(filesTreeView !== undefined ? { filesTreeView } : {}),
    ...(filesTreeIntent !== undefined ? { filesTreeIntent } : {}),
    ...(reviewLens !== undefined ? { reviewLens } : {}),
    ...(transcriptViewport !== undefined ? { transcriptViewport } : {}),
  });
}

function emptyReset(reason: string): DenAppStateV1 {
  console.warn(`[app-state] reset empty: ${reason}`);
  return { ...EMPTY_APP_STATE_V1 };
}

/** Parse and validate the current persisted app-state shape. */
export function parseAppState(raw: unknown): DenAppStateV1 {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return emptyReset("corrupt or missing document");
  }

  const doc = raw as AppStateDoc;
  const version = doc.version;
  if (version !== APP_STATE_VERSION) {
    return emptyReset(`invalid version ${String(version)}`);
  }

  return parseCurrentAppState(doc);
}
