import { loadSourceCorpus } from "../test/source-corpus.ts";
import { readSourceText } from "../test/stylesheet-source.ts";
import { execSync } from "node:child_process";
import { join } from "node:path";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { expect } from "vitest";

const shellDir = join(dirname(fileURLToPath(import.meta.url)));
export const denSrc = join(shellDir, "..");

export type InvariantClass = "forbidden" | "required";

export type InvariantEntry = {
  id: string;
  class: InvariantClass;
  pattern?: string;
  roots?: string;
  allowGlobs?: string[];
  note?: string;
  structural?: () => void;
};

const PROJECTS_REGISTRY_ALLOWLIST = ["platform/connection/app-connection.ts"];

/** Combines the shell with its sibling controllers and components. */
function shellSource(): string {
  const parts = loadSourceCorpus(join(denSrc, "components/shell"), { extensions: [".ts", ".tsx"], excludeTests: true })
    .select((file) => /^(?:shell-[\w-]+\.ts|Shell\w+\.tsx)$/.test(file.rel));
  return [readSourceText(join(denSrc, "components/shell/Shell.tsx")), ...parts.map((file) => file.text)].join("\n");
}

export function relativeToDenSrc(absPath: string): string {
  const norm = absPath.replace(/\\/g, "/");
  const root = denSrc.replace(/\\/g, "/") + "/";
  return norm.startsWith(root) ? norm.slice(root.length) : norm;
}

export function isAllowlisted(line: string, allowGlobs: string[]): boolean {
  const pathPart = line.split(":")[0] ?? line;
  const rel = relativeToDenSrc(pathPart);
  return allowGlobs.some((glob) => rel.endsWith(glob) || rel.includes(glob));
}

export function rgMatches(
  pattern: string,
  roots: string,
  excludeGlobs: string[] = ["*den-client-state-invariants*"],
): string[] {
  const excludes = excludeGlobs.map((g) => `-g '!${g}'`).join(" ");
  const out = execSync(
    `rg -n '${pattern}' "${roots}" ${excludes} 2>/dev/null || true`,
    { encoding: "utf8" },
  ).trim();
  if (!out) return [];
  return out.split("\n").filter((line) => line.length > 0);
}

export function rgForbidden(
  pattern: string,
  roots: string,
  opts?: { allowGlobs?: string[]; excludeGlobs?: string[] },
): string[] {
  const matches = rgMatches(pattern, roots, opts?.excludeGlobs);
  const allow = opts?.allowGlobs ?? [];
  return matches.filter((m) => !isAllowlisted(m, allow));
}

export function rgRequired(
  pattern: string,
  roots: string,
  opts?: { excludeGlobs?: string[] },
): string[] {
  return rgMatches(pattern, roots, opts?.excludeGlobs);
}

function sliceRunResumeSession(src: string): string {
  return src.slice(
    src.indexOf("export async function runResumeSession"),
    src.indexOf("export async function runCreateSession"),
  );
}

function sliceRunCreateSession(src: string): string {
  return src.slice(src.indexOf("export async function runCreateSession"));
}

function assertNav07(): void {
  const src = readSourceText(join(denSrc, "chat/session/session-switch.ts"));
  const create = sliceRunCreateSession(src);
  // Complete hydration before deferred enrichment.
  const completeIdx = create.indexOf("completeChatSessionHydration");
  const enrichIdx = create.indexOf("const enrich = params.enrich");
  expect(completeIdx).toBeGreaterThan(-1);
  expect(enrichIdx).toBeGreaterThan(-1);
  expect(completeIdx).toBeLessThan(enrichIdx);
  expect(create.slice(0, completeIdx)).not.toMatch(/await params\.enrich/);
  expect(create).not.toMatch(/await params\.hydrate/);
  expect(create).toMatch(/deferSessionFollowup[\s\S]*await enrich/);
}

function assertNav08(): void {
  const lifecycle = readSourceText(join(denSrc, "chat/session/session-lifecycle.ts"));
  const enrichStart = lifecycle.indexOf("export async function enrichCreatedSession");
  expect(enrichStart).toBeGreaterThan(-1);
  const enrichNext = lifecycle.indexOf("\nexport ", enrichStart + 1);
  const enrichFn = lifecycle.slice(
    enrichStart,
    enrichNext === -1 ? undefined : enrichNext,
  );
  expect(enrichFn).not.toMatch(/resumeProjectEventsAfter/);

  // Session navigation builds the create flow from session-creation.ts.
  const navigation = readSourceText(join(denSrc, "components/shell/session-navigation.ts"));
  expect(navigation).toMatch(/const startEmptySession = createSessionCreation\(/);
  const creation = readSourceText(join(denSrc, "components/shell/session-creation.ts"));
  const createStart = creation.indexOf("runCreateSession({");
  expect(createStart).toBeGreaterThan(-1);
  const createEnrich = creation.slice(createStart);
  expect(createEnrich.indexOf("enrich:")).toBeGreaterThan(-1);
  const enrichCall = createEnrich.slice(
    createEnrich.indexOf("enrich:"),
    createEnrich.indexOf("afterEnrich:"),
  );
  expect(enrichCall).toMatch(/enrichCreatedSession/);
  expect(enrichCall).not.toMatch(/resumeChatSession/);

  const boot = readSourceText(join(denSrc, "platform/connection/app-boot.ts"));
  expect(boot).toMatch(/enrichCreatedSession/);
  const bootCreate = boot.slice(boot.indexOf("runCreateSession({"));
  const bootEnrich = bootCreate.slice(
    bootCreate.indexOf("enrich:"),
    bootCreate.indexOf("onOpened:"),
  );
  expect(bootEnrich).toMatch(/enrichCreatedSession/);
  expect(bootEnrich).not.toMatch(/resumeChatSession/);
}

function assertNav09(): void {
  const navigation = readSourceText(join(denSrc, "components/shell/session-navigation.ts"));
  expect(navigation).toMatch(/const startEmptySession = createSessionCreation\(/);
  const creation = readSourceText(join(denSrc, "components/shell/session-creation.ts"));
  const start = creation.slice(
    creation.indexOf("const startEmptySession"),
    creation.indexOf("return startEmptySession;"),
  );
  const fence = start.indexOf("shellSessionSwitchGeneration.next()");
  const connect = start.indexOf("connectAppBackend");
  const createProject = start.indexOf("activeClient.createProject");
  const currentCheck = start.indexOf(
    "if (!creationIsCurrent()) return",
    createProject,
  );
  const upsert = start.indexOf("options.projects.upsert", createProject);
  expect(fence).toBeGreaterThan(-1);
  expect(fence).toBeLessThan(connect);
  expect(createProject).toBeGreaterThan(-1);
  expect(currentCheck).toBeGreaterThan(createProject);
  expect(currentCheck).toBeLessThan(upsert);
}

function assertNav10(): void {
  const shell = shellSource();
  const navStart = shell.indexOf("<FocusedProjectNav");
  expect(navStart).toBeGreaterThan(-1);
  const nav = shell.slice(navStart, shell.indexOf("onNewChat=", navStart));
  expect(nav).toContain("activeChat={activeChat()}");
  const peerStart = shell.indexOf("const focusedPeerSubject");
  expect(peerStart).toBeGreaterThan(-1);
  const peer = shell.slice(peerStart, shell.indexOf("resolvePeerOpenSubject(", peerStart));
  expect(peer).toContain("const chat = activeChat();");
  expect(peer).not.toMatch(/presentedChat\(\)/);
}

function assertNav04(): void {
  const shellStore = readSourceText(join(denSrc, "store/shell-store.ts"));
  expect(shellStore).toMatch(/export type SwitchKind/);
  const src = readSourceText(join(denSrc, "chat/session/session-switch.ts"));
  expect(src).not.toMatch(/export type SwitchKind\s*=/);
  const resume = sliceRunResumeSession(src);
  expect(resume).toMatch(/params\.kind/);
  const crossStart = resume.indexOf('if (params.kind === "cross-project")');
  expect(crossStart).toBeGreaterThan(-1);
  const crossEnd = resume.indexOf("} else {", crossStart);
  const cross = resume.slice(crossStart, crossEnd);
  expect(cross.indexOf("clearChatForSessionSwitch")).toBeLessThan(
    cross.indexOf("await params.deps.prepareProject"),
  );
}

function assertNav05(): void {
  const src = readSourceText(join(denSrc, "chat/session/session-switch.ts"));
  const resume = sliceRunResumeSession(src);
  const crossStart = resume.indexOf('if (params.kind === "cross-project")');
  const crossEnd = resume.indexOf("} else {", crossStart);
  const cross = resume.slice(crossStart, crossEnd);
  expect(cross).not.toMatch(/beginSessionResumeSwitch/);
  expect(cross).not.toMatch(/restoreCachedSessionChat/);
  const same = resume.slice(crossEnd);
  expect(same).toMatch(/restoreCachedSessionChat/);
}

function assertNav06(): void {
  const shell = shellSource();
  expect(shell).toMatch(/deriveStageScope/);
  expect(shell).toMatch(/readyChat/);
  expect(shell).toMatch(/showChatTabRail/);
  expect(shell).toMatch(/useResidentStack/);
  expect(shell).toMatch(/ResidentSurface/);
  expect(shell).toMatch(/holdPresentedChat/);
  expect(shell).toMatch(/restoreCachedSessionChat/);
  const navigation = readSourceText(join(denSrc, "components/shell/session-navigation.ts"));
  expect(navigation).toMatch(/prepareProjectScope/);
  expect(readSourceText(join(denSrc, "platform/connection/app-boot.ts"))).toMatch(/prepareProjectScope/);
  expect(readSourceText(join(denSrc, "chat/session/prepare-project.ts"))).toMatch(
    /if \(alreadyLive\) return/,
  );
  expect(navigation).toMatch(
    /keepStage = opts\?\.keepStage \?\? options\.conversationInLayout\(\)/,
  );
  expect(shell).toMatch(/chatStack/);
  expect(shell).toMatch(/stageStack/);
  expect(shell).toMatch(/ChatTabChromeProvider/);
  expect(shell).toMatch(/const ChatStageBody/);
  expect(shell).toMatch(/const ChatStageLayer/);
  expect(shell).toMatch(/const StageSurface/);
  expect(shell).toMatch(/<ChatView[\s\S]*projects=\{props\.projects\}/);
  const chatViewIdx = shell.indexOf("<ChatView");
  expect(chatViewIdx).toBeGreaterThan(-1);
  const beforeChatView = shell.slice(Math.max(0, chatViewIdx - 400), chatViewIdx);
  expect(beforeChatView).not.toMatch(/activeChat\(\)/);
  // Each chat layer reads its own key's presence in the chat stack.
  const columns = readSourceText(join(denSrc, "components/shell/ShellColumns.tsx"));
  expect(columns).toMatch(/props\.children\(chat, \(\) => props\.stack\.presence\(key\)\)/);
  expect(shell).toMatch(/<ShellChatColumn[\s\S]*stack=\{chatStack\}/);
  expect(shell).toMatch(
    /<ChatStageLayer[\s\S]*pending=\{\(\) => presence\(\) === "pending"\}/,
  );
  expect(shell).toMatch(/surfaceActive=\{\(\) => presence\(\) !== "idle"\}/);
  expect(shell).toMatch(
    /const chatColDual = \(\) => chatStack\.pending\(\) != null/,
  );
  expect(shell).not.toMatch(/const chatColDual = \(\) =>\s*splitLive\(\) &&/);
  expect(shell).toMatch(/"den-stage-boot": layer\.pending\(\) === true/);
  const stageColumnStart = columns.indexOf('aria-label="Stage"');
  const chatColumnStart = columns.indexOf("export function ShellChatColumn");
  expect(stageColumnStart).toBeGreaterThan(-1);
  expect(chatColumnStart).toBeGreaterThan(stageColumnStart);
  expect(columns.slice(stageColumnStart, chatColumnStart)).not.toMatch(/ChatStage/);
}

function assertFocusKey01(): void {
  const shell = shellSource();
  expect(shell).toMatch(/stabilizeActiveChat/);
  expect(shell).toMatch(/holdPresentedChat/);
  expect(shell).toMatch(/presentedChat/);
  expect(shell).toMatch(/readyChat/);
  expect(shell).toMatch(/const railStatusProject = createMemo/);
  expect(shell).toMatch(/<Show when=\{railStatusProject\(\)\} keyed>/);
  expect(shell).toMatch(/sessionId=\{railStatusSessionId\(projectId\)\}/);

  const files = readSourceText(join(denSrc, "files/components/ProjectFilesView.tsx"));
  const deck = readSourceText(join(denSrc, "files/components/FilesPaneDeck.tsx"));
  // The resident deck mounts one pane per primitive pane key, never per buffer object.
  expect(files).toMatch(/return filesPaneKey\(key, buf\.kind\);/);
  expect(files).toMatch(/activePane=\{activePane\(\) \?\? null\}/);
  expect(deck).toMatch(/<SurfaceDeck active=\{props\.activePane\}/);
  expect(deck).toMatch(/const parsed = parseFilesPaneKey\(paneKey\);/);
  expect(files + deck).not.toMatch(/return \{ key, kind: buf\.kind \}/);
  const paneKey = readSourceText(join(denSrc, "files/components/files-pane-key.ts"));
  expect(paneKey).toMatch(/kind: FileBufferKind,\n\): string \{/);

  const host = readSourceText(join(denSrc, "files/editor/files-editor-host.ts"));
  expect(host).toMatch(/softDetachFilesEditor/);
  expect(host).toMatch(/beginFilesEditorAttach/);
  expect(host).toMatch(/SourceEditorHandlers/);

  const hotExit = readSourceText(join(denSrc, "files/documents/files-hot-exit.ts"));
  expect(hotExit).toMatch(/from "\.\.\/editor\/files-editor-host\.ts"/);

  const focus = readSourceText(join(denSrc, "shortcuts/focus-region.ts"));
  expect(focus).toMatch(/"find"/);
  expect(focus).toMatch(/"settings"/);

  const composer = readSourceText(join(denSrc, "components/chatview/Composer.tsx"));
  expect(composer).toMatch(/activeElementInFocusRegion/);
  expect(composer).toMatch(/isFocusRegionMounted\("settings"\)/);
  expect(composer).toMatch(/isFocusRegionMounted\("find"\)/);
  expect(composer).not.toMatch(/querySelector\("\.den-settings-panel"\)/);
  expect(composer).not.toMatch(/findController\.isOpen/);
  expect(composer).not.toMatch(/closest\('\[data-testid="find-bar"\]'\)/);

  const findBar = readSourceText(join(denSrc, "find/FindBar.tsx"));
  expect(findBar).toMatch(/registerFocusRegion\("find"/);

  const settings = readSourceText(
    join(denSrc, "components/settings/SettingsStagePanel.tsx"),
  );
  expect(settings).toMatch(/registerFocusRegion\("settings"/);
}

function assertCache01(): void {
  const src = readSourceText(join(denSrc, "store/app-state.ts"));
  expect(src).not.toMatch(/^\s*projects: Project\[\]/m);
  expect(src).not.toMatch(/setProjects/);
}

function assertCache03(): void {
  const registryHits = rgForbidden("projectsRegistry", join(denSrc, "src"), {
    allowGlobs: PROJECTS_REGISTRY_ALLOWLIST,
  });
  expect(
    registryHits,
    `projectsRegistry outside ${PROJECTS_REGISTRY_ALLOWLIST.join(", ")}\n${registryHits.join("\n")}`,
  ).toEqual([]);

  const shell = shellSource();
  expect(shell).toMatch(/projects=\{props\.projects\}/);

  const chatView = readSourceText(join(denSrc, "components/chatview/ChatView.tsx"));
  expect(chatView).toMatch(/projects:\s*ProjectsStore/);
}

function assertBoot01(): void {
  const boot = readSourceText(join(denSrc, "platform/connection/app-boot.ts"));
  expect(boot).toMatch(/lastActiveProjectId/);
  expect(boot).toMatch(/restoreLastActiveProject/);
}

function assertOpen01(): void {
  const shell = shellSource();
  expect(shell).toMatch(/ready\.mounted/);
  expect(shell).toMatch(/ready\.open/);
  expect(shell).toMatch(/<WorkspaceOpeningVeil\s+visible=\{!workspaceRevealed\(\)\}(?:\s|>)/);
  // The rail's workspace regions paint on the reveal frame.
  expect(shell).toMatch(/sessionsLoaded=\{sidebarChatsPublished\(\)\}/);
  expect(shell).toMatch(/const sidebarChatsPublished = \(\) =>\s*workspaceRevealed\(\) &&/);
  expect(shell).toMatch(/statusRevealed=\{workspaceRevealed\(\)\}/);
  const nav = readSourceText(join(denSrc, "components/nav/FocusedProjectNav.tsx"));
  expect(nav).toMatch(/data-presentation=\{props\.statusRevealed === false \? "preparing" : "published"\}/);
  // The session board and its git read are part of the open.
  for (const file of ["platform/connection/app-boot.ts", "components/shell/session-creation.ts"]) {
    const source = readSourceText(join(denSrc, file));
    const afterEnrich = source.slice(source.indexOf("afterEnrich:"), source.indexOf("onOpened:"));
    expect(afterEnrich).toMatch(/workspacePreparation\([^)]*\)\.run\(\s*"session-board"/);
  }
  const rail = readSourceText(join(denSrc, "components/nav/ChatTabRail.tsx"));
  expect(rail).toMatch(/usePresentationParticipant\(\s*"git-status"/);
  const columns = readSourceText(join(denSrc, "components/shell/ShellColumns.tsx"));
  const stageColumn = columns.slice(
    columns.indexOf('aria-label="Stage"'),
    columns.indexOf("export function ShellChatColumn"),
  );
  expect(stageColumn).not.toMatch(/ProjectLoadingStage/);

  const veil = readSourceText(
    join(denSrc, "components/shell/WorkspaceOpeningVeil.tsx"),
  );
  expect(veil).toMatch(/den-shell-workspace-veil/);

  const files = readSourceText(join(denSrc, "files/components/ProjectFilesView.tsx"));
  expect(files).toMatch(/name: "files-stage"/);
  expect(shell).toMatch(/workspacePreparation\(projectId\)\.ready\(\)/);
}

function assertPlace01(): void {
  const types = readSourceText(join(denSrc, "../shared/app-state-types.ts"));
  expect(types).toMatch(
    /export type StagePlacement = "inline" \| "split"/,
  );
  const placement = readSourceText(join(denSrc, "shell/stage-placement.ts"));
  expect(placement).toMatch(
    /export const DEFAULT_PLACEMENT: StagePlacement = "inline"/,
  );
  const shell = shellSource();
  const saves = shell.match(/saveStagePlacementMode\(/g) ?? [];
  expect(saves).toHaveLength(1);
  expect(shell).toContain("const applyStagePlacement");
  // A repeat press reads the committed placement, so the commit cannot wait on the window.
  const placementSource = readSourceText(join(denSrc, "components/shell/shell-stage-placement.ts"));
  const apply = placementSource.slice(
    placementSource.indexOf("const applyStagePlacement"),
    placementSource.indexOf("// Startup width is claimed"),
  );
  expect(apply).toContain("saveStagePlacementMode(");
  expect(apply).not.toMatch(/await claimStageChrome/);
  expect(shell).toMatch(
    /layout\.toggleSplit[\s\S]*?applyStagePlacement/,
  );
}

function assertPlace02(): void {
  const catalog = readSourceText(join(denSrc, "../shared/app-state-types.ts"));
  expect(catalog).toMatch(/CONTEXT_NAV_CATALOG/);
  const registry = readSourceText(join(denSrc, "components/stage/stage-registry.tsx"));
  expect(registry).toMatch(/export const STAGE_REGISTRY: Record<ContextNavItemId/);
  expect(registry).toContain("search:");
  expect(registry).toContain("files:");
  expect(registry).toContain("security:");
  expect(registry).toContain("artifacts:");
  expect(registry).toContain("blueprints:");
  expect(registry).toContain("extensions:");
}

function assertPlace03(): void {
  const shell = shellSource();
  expect(shell).toMatch(/openItemWindow/);
  expect(shell).toMatch(/itemWindowViews/);
}

function assertSplit01(): void {
  const placement = readSourceText(join(denSrc, "shell/stage-placement.ts"));
  expect(placement).toContain("export function resolveStageColumn");
  expect(placement).toMatch(
    /export const DEFAULT_SPLIT_COMPANION: ContextNavItemId = "files"/,
  );
  const shell = shellSource();
  expect(shell).toMatch(/resolveStageColumn/);
  expect(shell).toMatch(/stageColumn\(\)\.splitLive/);
}

function assertSplit02(): void {
  const placement = readSourceText(join(denSrc, "shell/stage-placement.ts"));
  expect(placement).toMatch(/SPLIT_/);
  const store = readSourceText(join(denSrc, "shell/layout-store.ts"));
  expect(store).toMatch(/setSplitHostWidth/);
  const shell = shellSource();
  expect(shell).not.toMatch(/isSplitAvailable/);
}

function assertSplit04(): void {
  const collapse = readSourceText(join(denSrc, "shell/responsive-collapse.ts"));
  expect(collapse).toContain("export function stageOpenWindowDeficitPx");
  const dock = readSourceText(join(denSrc, "components/shell/LayoutDock.tsx"));
  const tile = dock.slice(dock.indexOf("function PlacementTile"));
  expect(tile.length).toBeGreaterThan(0);
  expect(tile).not.toMatch(/disabled/);
  const shell = shellSource();
  expect(shell).toContain("const claimStageChrome");
  expect(shell).toMatch(/widenWindowBy\(stageChromeDeficitPx\(/);
  expect(shell).toMatch(/claimStageChrome\(placement === "split"/);
  expect(shell).toMatch(/claimStageChrome\(stagePlacementMode\(\) === "split"/);
}

function assertSplit05(): void {
  const types = readSourceText(join(denSrc, "../shared/app-state-types.ts"));
  expect(types).toMatch(/chatWidthPx\?: number/);
  const store = readSourceText(join(denSrc, "shell/layout-store.ts"));
  expect(store).toMatch(/export function preferredChatWidthPx\(\)/);
  const collapse = readSourceText(join(denSrc, "shell/responsive-collapse.ts"));
  expect(collapse).toContain("export function workspaceStageFloorPx");
}

function assertSplit03(): void {
  const css = readSourceText(join(denSrc, "global-components.css"));
  expect(css).toMatch(/@container den-tab-chips/);
}

function assertView01(): void {
  const subject = readSourceText(join(denSrc, "platform/windows/window-subject.ts"));
  const registry = readSourceText(join(denSrc, "platform/windows/workspace-view-registry.ts"));
  const host = readSourceText(join(denSrc, "../src-tauri/src/item_windows.rs"));
  expect(subject).toMatch(/WindowSubject[\s\S]*viewId: string/);
  expect(registry).toMatch(/setCurrentWorkspaceContexts/);
  expect(host).toMatch(/Window \{view_id\}/);
}

function assertView02(): void {
  const items = readSourceText(join(denSrc, "../src-tauri/src/item_windows.rs"));
  const appearance = readSourceText(join(denSrc, "../src-tauri/src/window_appearance.rs"));
  expect(items).toMatch(/close_item_window/);
  expect(appearance).toMatch(/CloseRequested[\s\S]*main\.hide\(\)/);
}

function assertFilesChat01(): void {
  const view = readSourceText(join(denSrc, "files/components/ProjectFilesView.tsx"));
  // Chat changes invalidate workspace resolution, not file acquisition.
  expect(view).toMatch(/const sourceSessionId = \(\) => untrack\(chatSessionId\);/);
  expect(view).toMatch(/createFilesWorkspace\(\{[^}]*chatSessionId/);
  expect(view).toMatch(/<FilesTree[\s\S]*?sessionId=\{filesSourceAddress\(\)\}/);
  // A prop taking the untracked read would freeze on the chat that mounted the stage.
  expect(view).not.toMatch(/=\{sourceSessionId\(\)\}/);
  const workspace = readSourceText(join(denSrc, "files/source/files-workspace.ts"));
  // Workspace identity is independent of the selected chat.
  const scopeKey = /const scopeKey = JSON\.stringify\(\[(.*)\]\);/.exec(workspace)?.[1];
  expect(scopeKey).toBeDefined();
  expect(scopeKey).not.toMatch(/session|chat/i);
  expect(workspace).toMatch(/const address = workspace\.session_scoped \? sessionId : undefined;/);
}

export const INVARIANT_CATALOG: InvariantEntry[] = [
  {
    id: "INV-NAV-01",
    class: "forbidden",
    pattern: "setActiveProject",
    roots: join(denSrc, "src"),
    note: "Switch-intent API only — beginStageSwitch / commitStageScope",
  },
  {
    id: "INV-NAV-02",
    class: "forbidden",
    pattern: "let activeProjectId",
    roots: join(denSrc, "src/platform/connection/app-connection.ts"),
    note: "The app connection is the active-project authority",
  },
  {
    id: "INV-NAV-03",
    class: "required",
    pattern: "deriveStageScope",
    roots: join(denSrc, "components/shell/Shell.tsx"),
    note: "Main stage reads deriveStageScope",
  },
  {
    id: "INV-NAV-04",
    class: "required",
    structural: assertNav04,
    note: "runResumeSession SwitchKind + clearChat before hydrate on cross-project",
  },
  {
    id: "INV-NAV-05",
    class: "forbidden",
    structural: assertNav05,
    note: "Cross-project path must not call beginSessionResumeSwitch or restoreCachedSessionChat",
  },
  {
    id: "INV-NAV-07",
    class: "required",
    structural: assertNav07,
    note: "runCreateSession unlocks before deferred enrich (no await enrich/hydrate)",
  },
  {
    id: "INV-NAV-08",
    class: "required",
    structural: assertNav08,
    note: "Create enrich uses enrichCreatedSession (no resumeProjectEventsAfter / resumeChatSession)",
  },
  {
    id: "INV-NAV-09",
    class: "required",
    structural: assertNav09,
    note: "Session creation is fenced before backend work and stale results stay out of local state",
  },
  {
    id: "INV-NAV-10",
    class: "required",
    structural: assertNav10,
    note: "Sidebar selection and the peer-window subject follow the selected chat, not the painted one",
  },
  {
    id: "INV-NAV-06",
    class: "required",
    structural: assertNav06,
    note: "ChatView renders under ChatTabChromeProvider in the resident chat stack",
  },
  {
    id: "INV-FOCUS-KEY-01",
    class: "required",
    structural: assertFocusKey01,
    note: "Focus hosts use primitive/stabilized keyed Show identities; Files controls EditorView via host; composer respects focus regions",
  },
  {
    id: "INV-CACHE-01",
    class: "forbidden",
    structural: assertCache01,
    note: "Project state has no projects cache",
  },
  {
    id: "INV-CACHE-02",
    class: "forbidden",
    pattern: "projectIdForPath\\(appStore\\.state\\.projects",
    roots: join(denSrc, "src"),
  },
  {
    id: "INV-CACHE-03",
    class: "required",
    structural: assertCache03,
    note: "projectsRegistry only in app-connection; UI threads ProjectsStore",
  },
  {
    id: "INV-BOOT-01",
    class: "required",
    structural: assertBoot01,
    note: "lastActiveProjectId wired in boot restore",
  },
  {
    id: "INV-OPEN-01",
    class: "required",
    structural: assertOpen01,
    note: "Workspace preparation is scoped to its project and reveals once its regions report content; rail chips and chat list paint on that frame",
  },
  {
    id: "INV-PLACE-01",
    class: "required",
    structural: assertPlace01,
    note: "StagePlacement is exactly inline | split",
  },
  {
    id: "INV-PLACE-02",
    class: "required",
    structural: assertPlace02,
    note: "Placeable set equals CONTEXT_NAV_CATALOG; STAGE_REGISTRY is total",
  },
  {
    id: "INV-PLACE-03",
    class: "required",
    structural: assertPlace03,
    note: "Pulled-off views open via openItemWindow / itemWindowViews",
  },
  {
    id: "INV-SPLIT-01",
    class: "required",
    structural: assertSplit01,
    note: "resolveStageColumn controls splitLive",
  },
  {
    id: "INV-SPLIT-02",
    class: "required",
    structural: assertSplit02,
    note: "Narrow host collapses visually without writing inline",
  },
  {
    id: "INV-SPLIT-03",
    class: "forbidden",
    structural: assertSplit03,
    note: "Chat tab chips collapse via container query only",
  },
  {
    id: "INV-SPLIT-04",
    class: "required",
    structural: assertSplit04,
    note: "Split is offered at any width; a narrow window is widened by the deficit",
  },
  {
    id: "INV-SPLIT-05",
    class: "required",
    structural: assertSplit05,
    note: "Split ratio is one device preference; live-split chrome uses the Files floor",
  },
  {
    id: "INV-FILES-01",
    class: "required",
    structural: assertFilesChat01,
    note: "Files addresses a chat, never keys on it; only a session-scoped workspace names one",
  },
  {
    id: "INV-VIEW-01",
    class: "required",
    structural: assertView01,
    note: "Peer views keep an immutable subject and host-assigned window slot",
  },
  {
    id: "INV-VIEW-02",
    class: "required",
    structural: assertView02,
    note: "Peer close is scoped; macOS main close hides instead of quitting",
  },
];

export function assertInvariant(entry: InvariantEntry): void {
  if (entry.structural) {
    entry.structural();
    return;
  }
  if (!entry.pattern || !entry.roots) {
    throw new Error(`${entry.id}: missing pattern/roots and no structural hook`);
  }
  if (entry.class === "forbidden") {
    const violations = rgForbidden(entry.pattern, entry.roots, {
      allowGlobs: entry.allowGlobs,
    });
    expect(
      violations,
      `${entry.id}${entry.note ? `: ${entry.note}` : ""}\n${violations.join("\n")}`,
    ).toEqual([]);
    return;
  }
  const hits = rgRequired(entry.pattern, entry.roots);
  expect(
    hits.length,
    `${entry.id}${entry.note ? `: ${entry.note}` : ""} — required pattern ${entry.pattern} missing in ${entry.roots}`,
  ).toBeGreaterThan(0);
}
