import { readSourceText } from "../../test/stylesheet-source.ts";
import { join } from "node:path";
import { expect } from "vitest";
import {
  denSrc,
  shellSource,
  type InvariantEntry,
} from "./common.ts";

function sliceRunResumeSession(src: string): string {
  return src.slice(
    src.indexOf("export async function runResumeSession"),
    src.indexOf("export async function runCreateSession"),
  );
}

function sliceRunCreateSession(src: string): string {
  return src.slice(src.indexOf("export async function runCreateSession"));
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

export const NAV_INVARIANTS: InvariantEntry[] = [
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
];
