/** Persisted desktop app-state slices (`~/.config/paintedwolf/app-state-v1/`). */

export const APP_STATE_VERSION = 1 as const;
export const DEFAULT_SESSION_TITLE = "New session" as const;

export type RecentSessionInput = {
  projectId: string;
  sessionId: string;
  title?: string;
};

export type RecentSession = RecentSessionInput & {
  projectId: string;
  title: string;
  /** Set when the user sends a prompt; drives list order and relative-time labels. */
  lastActivityAt?: number;
};

export type DenDebugPrefs = {
  /** When true, show benign tool rejects in chat (off by default). */
  verboseMode?: boolean;
  /** Enable full backend and UI performance capture after restart. */
  fullDebugLogging?: boolean;
};

/** Responsive pane layout with independent width and height. */
export type DenListPanePrefs = {
  /** Detail pane width when the pane sits beside the list (px). */
  widthPx?: number;
  /** Detail pane height when the pane is stacked under the list (px). */
  heightPx?: number;
  /** Column key → width (px). Unknown keys are pruned against the spec. */
  columnWidths?: Record<string, number>;
  /** Active sort column; absent means the surface's own default order. */
  sortKey?: string;
  sortDir?: "asc" | "desc";
};

/** Surface id (`search`, `security`) → its list layout. */
export type DenListPanesPrefs = Record<string, DenListPanePrefs>;

/** Persisted shell, workspace, and pane layout. */
export type DenLayoutPrefs = {
  navWidthPx?: number;
  /** When true, the main navigation rail is fully hidden. */
  navCollapsed?: boolean;
  /** Explicitly hidden split pane. Placement changes reveal both panes. */
  hiddenSplitPane?: SplitPane;
  /** Main-detail list stages keyed by surface id. */
  listPanes?: DenListPanesPrefs;
  /** Main workspace placement. */
  mode?: StagePlacement;
  /** Split companion. Kept across inline. */
  companion?: ContextNavItemId;
  /** Context surface to open beside chat at launch. */
  startupCompanion?: ContextNavItemId;
  /**
   * Column a split keeps when the host is too narrow for both. Absent follows
   * {@link DenLayoutPrefs.startupCompanion}.
   */
  narrowSurvivor?: NarrowSplitSurvivor;
  /** Reflects the whole workspace, including navigation and stage interiors. */
  workspaceOrientation?: WorkspaceOrientation;
  /** Column order away from the sidebar, before workspace mirroring. */
  splitOrder?: SplitOrder;
  /** Conversation column width in a split; the stage absorbs window resizes. */
  chatWidthPx?: number;
};

export const LIST_PANE_WIDTH_DEFAULT_PX = 360;
export const LIST_PANE_WIDTH_MIN_PX = 280;
export const LIST_PANE_WIDTH_MAX_PX = 720;

export const LIST_PANE_HEIGHT_DEFAULT_PX = 280;
export const LIST_PANE_HEIGHT_MIN_PX = 160;
export const LIST_PANE_HEIGHT_MAX_PX = 720;

/* Matches --den-files-tree-w. */
export const FILES_TREE_WIDTH_DEFAULT_PX = 240;
export const FILES_TREE_WIDTH_MIN_PX = 180;

/** Client-side display preferences (no backend). */
export type DenDisplayPrefs = {
  /** When true, inline diff previews wrap long lines (on by default). */
  diffWordWrap?: boolean;
  /** When true, inline diff previews start fully collapsed to the file header (on by default). */
  diffCollapsed?: boolean;
  /** When true, the fullscreen diff viewer renders side-by-side (unified by default). */
  diffSplit?: boolean;
  /** Shows role-excluded and below-floor models in assignment pickers; defaults off. */
  showAllModels?: boolean;
};

/** Client-side OS notification preferences (no backend). Defaults all-on. */
export type DenNotificationPrefs = {
  /** Main switch; default true. */
  enabled?: boolean;
  /** Session finished a meaningful run; default true. */
  finished?: boolean;
  /** Agent parked on pending user input; default true. */
  needsYou?: boolean;
  /** HITL checkpoint waiting for approval; default true. */
  needsApproval?: boolean;
};

/** Context navigation visibility and order are device-wide. */
export type ContextNavItemId =
  | "search"
  | "files"
  | "security"
  | "cost"
  | "artifacts"
  | "blueprints"
  | "extensions";

/** Default order of Context entries. */
export const CONTEXT_NAV_CATALOG: readonly ContextNavItemId[] = [
  "files",
  "search",
  "security",
  "cost",
  "artifacts",
  "blueprints",
  "extensions",
] as const;

/** Default pinned set — Artifacts and Extensions start hidden. */
export const DEFAULT_CONTEXT_NAV_VISIBLE: readonly ContextNavItemId[] = [
  "files",
  "search",
  "security",
  "cost",
  "blueprints",
] as const;

/** Pinned Context entries in the project nav (device-wide). */
export type DenContextNavPrefs = {
  /** Ordered ids shown in Context. Absent → {@link DEFAULT_CONTEXT_NAV_VISIBLE}; empty shows none. */
  visible?: ContextNavItemId[];
};

/** First-run provider setup latch. */
export type DenOnboardingPrefs = {
  /** Set true when the OnboardingGate welcome page is dismissed. */
  firstRunSetupCompleted?: boolean;
};

/** What's New dismiss latch — version whose notes the user acknowledged. */
export type DenWhatsNewPrefs = {
  lastSeenVersion?: string;
};

/** Device-local controls and acknowledgements for contextual first-time tips. */
export type DenFirstTimeTipsPrefs = {
  /** Main switch; tips are enabled until explicitly disabled. */
  enabled?: boolean;
  /** Catalog ids acknowledged by the user. Unknown ids are discarded on load. */
  dismissed?: string[];
};

/** Which scheme the window paints, and the theme chosen for each. */
export type DenAppearancePrefs = {
  /** `system` pairs one theme per scheme; `light`/`dark` pin one. */
  mode?: "light" | "dark" | "system";
  /** Contribution id of the theme used when the scheme is light. */
  lightTheme?: string;
  /** Contribution id of the theme used when the scheme is dark. */
  darkTheme?: string;
  /** System, bundled, or custom font family; unavailable names remain selected. */
  uiFont?: string;
  /** Mono font, same three forms. Drives the editor unless it overrides. */
  monoFont?: string;
  /** Product multiplier composed with the operating-system accessibility scale. */
  textScale?: ProductTextScale;
};

/** Reading-size choices offered for a device. */
export const PRODUCT_TEXT_SCALES = [0.9, 1, 1.15, 1.3] as const;
export type ProductTextScale = (typeof PRODUCT_TEXT_SCALES)[number];

export function isProductTextScale(value: unknown): value is ProductTextScale {
  return typeof value === "number" && PRODUCT_TEXT_SCALES.includes(value as ProductTextScale);
}

/** Device shortcut overrides keyed by declaration ID. */
export type DenShortcutPrefs = {
  /** Canonical chord or leader sequence. */
  overrides?: Record<string, string>;
};

/** When a person's own messages show their time in the chat transcript. */
export type DenMessageTimes = "hover" | "always";

/** How the sidebar orders a project's unpinned chats. */
export const CHAT_LIST_SORTS = ["created", "activity", "title"] as const;
export type ChatListSort = (typeof CHAT_LIST_SORTS)[number];
/** Creation order keeps rows where they are. */
export const DEFAULT_CHAT_LIST_SORT: ChatListSort = "created";

/** Sidebar chat list preferences (device-wide, every project). */
export type DenChatListPrefs = {
  sort?: ChatListSort;
};

export type DenChatPrefs = {
  /** Day labels and turn finishes always show; this governs the time beside each message. */
  messageTimes?: DenMessageTimes;
};

/** External-editor presets validated against the host catalog. */
export type ExternalEditorPreset =
  | "cursor"
  | "vscode"
  | "vscode-insiders"
  | "vscodium"
  | "windsurf"
  | "sublime"
  | "bbedit"
  | "textmate"
  | "coteditor"
  | "nova"
  | "zed"
  | "xcode"
  | "intellij-idea"
  | "webstorm"
  | "pycharm"
  | "goland"
  | "rubymine"
  | "phpstorm"
  | "clion"
  | "android-studio"
  | "rider"
  | "macvim"
  | "neovide"
  | "custom";
export type BrowserPreset =
  | "system-default"
  | "chrome"
  | "firefox"
  | "safari"
  | "custom";

/** Host-detected external-editor preset. */
export type DetectedEditorId = Exclude<ExternalEditorPreset, "custom">;

/** Installed external editor reported by the host. */
export type FoundEditor = {
  id: DetectedEditorId;
  /** User-facing label for the Settings dropdown. */
  displayName: string;
  /** Install path or `cli:<name>` when found via PATH. */
  installPath: string;
};

export type DenExternalOpenPrefs = {
  externalEditor?: ExternalEditorPreset;
  customOpenCommand?: string;
  customOpenFolderCommand?: string;
  /** Browser for web links. */
  browser?: BrowserPreset;
  customBrowserCommand?: string;
};

/** Indentation style preference when a file's own indent can't be detected. */
export type DenEditorIndentStyle = "spaces" | "tabs";
export type DenEditorVersionComparison = "current" | "before";

export type DenWalkControlsPlacement = "floating" | "docked";
export type DenWalkControlsLocation =
  | { placement: "docked" }
  | { placement: "floating"; position?: { x: number; y: number } };

export const DEN_FILES_TREE_LEVELS = { min: 3, max: 10, default: 5 } as const;

export type DenEditorKeymap = "emacs" | "vim";

export type DenEditorPrefs = {
  keymap?: DenEditorKeymap;
  tabMovesFocus?: boolean;
  /** When true, long lines wrap in the in-app file viewer (off by default). */
  wordWrap?: boolean;
  revealInTree?: boolean;
  /** Maximum sticky tree rows, including the root and any collapsed row; zero disables sticky rows. */
  treeVisibleLevels?: number;
  /** When true, the in-app file viewer shows line numbers (on by default). */
  lineNumbers?: boolean;
  /** Editor font size in px (10–20, default 13). */
  fontSize?: number;
  /** Draw indentation guides (on by default). */
  indentGuides?: boolean;
  /** Highlight whitespace (off by default). */
  whitespace?: boolean;
  /** Fallback indent style when detection is undecided (default spaces). */
  indentStyle?: DenEditorIndentStyle;
  /** Fallback indent width 2 | 4 | 8 (default 4). */
  indentWidth?: number;
  /**
   * Mono font family. Enumerated names or free text; unresolvable values fall
   * back to `var(--den-font-mono)` at render.
   */
  fontFamily?: string;
  /** Line height multiplier (1.1–2.0, step 0.05, default 1.5). */
  lineHeight?: number;
  /** Default historical-version comparison. */
  versionComparison?: DenEditorVersionComparison;
  walkControlsDefault?: DenWalkControlsPlacement;
  /** Last explicit placement on this device; coordinates are relative to the editor pane. */
  walkControlsLocation?: DenWalkControlsLocation;
  /** Paint the scrollbar overview ticks (on by default). */
  scrollbarTicks?: boolean;
  /** Tick kinds shown while scrollbar ticks are on (each on by default). */
  scrollbarTickKinds?: DenEditorScrollbarTickKinds;
  /** Show what agent chats are doing in files (on by default). */
  agentActivity?: boolean;
  /** Agent activity kinds shown while agent activity is on (each on by default). */
  agentActivityKinds?: DenEditorAgentActivityKinds;
};

export type DenEditorAgentActivityKinds = {
  /** Lines, whole files, and search matches agents read this turn. */
  reads?: boolean;
  /** Changes an agent or worker is about to land. */
  changes?: boolean;
  /** Agent chips and name highlights in the tree, tabs, and open files. */
  fileNames?: boolean;
};

export type DenEditorScrollbarTickKinds = {
  symbols?: boolean;
  changes?: boolean;
  matches?: boolean;
  cursor?: boolean;
  windows?: boolean;
  agents?: boolean;
};

export type DenReaderViewState = {
  selection?: {
    ranges: Array<{ anchor: { section: string; row: number; offset: number; side: "before" | "after" }; head: { section: string; row: number; offset: number; side: "before" | "after" } }>;
    main: number;
  };
  viewport?: { rank: number; fraction: number; atEnd?: boolean };
};

/** Durable per-file editor view state (cursor / scroll / folds) with sha guard. */
export type DenEditorViewStateEntry = {
  sha: string;
  reader?: DenReaderViewState;
  cursor: { anchor: number; head: number };
  selections?: Array<{ anchor: number; head: number }>;
  mainSelection?: number;
  document?: {
    id: string;
    epoch: number;
    selections: Array<{ anchor: string; head: string }>;
    folds: Array<{ from: string; to: string }>;
  };
  scrollTop: number;
  folds: Array<{ from: number; to: number }>;
  capturedAt: number;
};

export type DenEditorViewStateStore = {
  /** Keyed by `${rootId}\u0000${path}`. */
  byKey: Record<string, DenEditorViewStateEntry>;
};

/** One closed Files tab eligible for reopen (most-recent first in the ring). */
export type DenClosedBufferEntry = {
  key: string;
  rootId: string;
  path: string;
  rootLabel?: string;
  viewState?: DenEditorViewStateEntry;
  pinned?: boolean;
  decodeAs?: "utf-16le" | "utf-16be";
};

export type DenClosedBufferRing = {
  byProject: Record<string, DenClosedBufferEntry[]>;
};

/** Cap of per-session unsent composer drafts kept in app-state. */
export const COMPOSER_DRAFTS_SESSION_CAP = 40;

/** Display-only project row persisted for instant home/nav paint. */
export type CachedProject = {
  id: string;
  name: string | null;
  roots: Array<{
    id: string;
    path: string;
    is_primary: boolean;
    label: string;
    added_at: string;
    kind: "draft" | "attached";
  }>;
  roots_generation: number;
  session_count: number;
  starred: boolean;
  is_draft: boolean;
  promotion: {
    destination_path: string;
    init_git: boolean;
    phase: "queued" | "staged" | "installed" | "committed";
    last_error: string;
    updated_at: string;
  } | null;
  last_activity_at?: string | null;
  last_opened_at: string;
  created_at: string;
};

/** Last observed row sizes used only as reload estimates. */
export type TranscriptRowHeightsSession = {
  projectId: string;
  sessionId: string;
  touchedAt: number;
  rows: Record<string, Record<string, number>>;
};

export type DenFilesHotExitBuffer = {
  documentId?: string;
  clientId?: string;
  rootId: string;
  path: string;
  kind: "text" | "image" | "info";
  rootLabel?: string;
  pinned?: boolean;
  preview?: boolean;
  decodeAs?: "utf-16le" | "utf-16be";
};

export type DenFilesHotExitProject = {
  buffers: DenFilesHotExitBuffer[];
  activeIndex?: number;
};

export type DenFilesHotExitState = {
  byProject: Record<string, DenFilesHotExitProject>;
};

export type DenFilesTreeIntent = {
  revision: string;
  disclosures: { address: { root_id: string; path: string }; open: boolean; recursive: boolean }[];
  touchedAt: number;
};

export type DenFilesTreeIntentState = {
  byWindow: Record<string, { byWorkspace: Record<string, DenFilesTreeIntent>; touchedAt: number }>;
};

/** Files tree scroll hints per window. */
export type DenFilesTreeViewState = {
  byWindow?: Record<
    string,
    {
      touchedAt: number;
      byProject: Record<string, { scrollTop?: number; touchedAt: number }>;
    }
  >;
};

/** Persisted Review-eye state for one project. */
export type DenReviewLensProjectState = {
  /** The eye's choice: `new`, `turn`, `session`, `commit`, or `pin:{id}`. Chat choices follow the selected chat. */
  comparison: string;
  /** Absent and false both mean comparison marking is on. */
  comparisonOff?: boolean;
  /** Absent and false both leave the person's own edits unmarked. */
  markMyEdits?: boolean;
  /** Absent means removed lines fold into the edge. */
  deletedLines?: "folded" | "inplace";
  touchedAt: number;
};

/** Last Review-eye choice, independently remembered for each project. */
export type DenReviewLensState = {
  byProject: Record<string, DenReviewLensProjectState>;
};

export type DenTranscriptViewportSnapshot = {
  /** Reading position by stable row; absent while following the latest message. */
  position?: { rowKey: string; rowOffsetPx: number };
  /** User-open disclosures. */
  openKeys: string[];
  touchedAt: number;
};

/** Saved transcript positions by window and session. */
export type DenTranscriptViewportState = {
  byWindow: Record<
    string,
    {
      touchedAt: number;
      bySession: Record<string, DenTranscriptViewportSnapshot>;
    }
  >;
};

export type DenAppStateV1 = {
  version: typeof APP_STATE_VERSION;
  recents: RecentSession[];
  lastActiveProjectId?: string;
  /** Stale-until-reconcile project summaries for boot-time home/nav. */
  cachedProjects?: CachedProject[];
  /** Last foreground session transcript snapshot for cold-boot restore. */
  lastSessionSnapshot?: unknown;
  /** Transcript row size hints keyed by session for the first reload layout. */
  transcriptRowHeights?: TranscriptRowHeightsSession[];
  debug?: DenDebugPrefs;
  /** User-adjustable layout preferences. */
  layout?: DenLayoutPrefs;
  display?: DenDisplayPrefs;
  /** Device-wide OS notification toggles (main + per-class). */
  notifications?: DenNotificationPrefs;
  /** Device-wide Context nav visibility + order (not per-project). */
  contextNav?: DenContextNavPrefs;
  onboarding?: DenOnboardingPrefs;
  /** Last What's New version the user dismissed (or was silently seeded). */
  whatsNew?: DenWhatsNewPrefs;
  /** Contextual first-time tip controls and acknowledgements. */
  firstTimeTips?: DenFirstTimeTipsPrefs;
  /** Appearance mode and the theme selected for each scheme. */
  appearance?: DenAppearancePrefs;
  shortcuts?: DenShortcutPrefs;
  chat?: DenChatPrefs;
  /** Sidebar chat list order. */
  chatList?: DenChatListPrefs;
  /** External destinations for files and links — Editor settings. */
  externalOpen?: DenExternalOpenPrefs;
  /** In-app source viewer display (word wrap, line numbers). */
  editor?: DenEditorPrefs;
  /** Unsent composer text keyed by session id. */
  composerDrafts?: Record<string, string>;
  /** Staged reference metadata; validated by composer attachment persistence. */
  composerAttachments?: unknown;
  /** Open Files tabs + dirty text drafts for hot exit (debounced persist). */
  filesHotExit?: DenFilesHotExitState;
  /** Persistent draft-promotion dismissals by project id. */
  draftPromoteDismissed?: string[];
  /** Persistent no-folder dismissals by project id. */
  nofolderDismissed?: string[];
  /** Per-file editor view state that survives close and restart (LRU-capped). */
  editorViewState?: DenEditorViewStateStore;
  /** Reopen-closed-tab ring per project (cap 20, most-recent first). */
  closedBufferRing?: DenClosedBufferRing;
  /** Files tree scroll hints. */
  filesTreeView?: DenFilesTreeViewState;
  filesTreeIntent?: DenFilesTreeIntentState;
  /** Review-eye comparison and on/off choice per project. */
  reviewLens?: DenReviewLensState;
  /** Saved transcript position and disclosure state. */
  transcriptViewport?: DenTranscriptViewportState;
};

/** How the main workspace lays out its foreground context. */
export type StagePlacement = "inline" | "split";

/** Split column that survives a host too narrow to carry both. */
export type SplitPane = "conversation" | "stage";
export type NarrowSplitSurvivor = SplitPane;

export type WorkspaceOrientation = "standard" | "mirrored";
export type SplitOrder = "context-first" | "chat-first";

/** Slice patch: present replaces, null deletes, and absent leaves unchanged. */
export type AppStatePatch = {
  [K in keyof Omit<DenAppStateV1, "version">]?: DenAppStateV1[K] | null;
};

export const EMPTY_APP_STATE_V1: DenAppStateV1 = {
  version: APP_STATE_VERSION,
  recents: [],
};

export const NAV_WIDTH_DEFAULT_PX = 280;
// Prevent the navigation identity block from wrapping.
export const NAV_WIDTH_MIN_PX = 208;
export const NAV_WIDTH_MAX_PX = 480;

export const RECENTS_CAP = 20;
export const CACHED_PROJECTS_CAP = 24;
export const TRANSCRIPT_ROW_HEIGHTS_SESSION_CAP = 8;
export const TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP = 800;
