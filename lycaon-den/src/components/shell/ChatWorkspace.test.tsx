import { render } from "@solidjs/testing-library";
import { For, createEffect, createSignal, type ComponentProps } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatWorkspace } from "./ChatWorkspace.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import type { ChatView } from "../chatview/ChatView.tsx";
import type { ChatTabRail } from "../nav/ChatTabRail.tsx";
import type { ChatTopChromeStack } from "../nav/ChatTopChromeStack.tsx";
import type { SpendCeilingReachedNudge } from "../SpendCeilingReachedNudge.tsx";
import type { SpendCeilingApproachingNudge } from "../SpendCeilingApproachingNudge.tsx";
import type { NoticeRail } from "../NoticeRail.tsx";
import type { SessionProtectionBanners } from "./SessionProtectionBanners.tsx";
import type { DraftPromoteBanner } from "../home/DraftPromoteBanner.tsx";
import type { NoFolderBanner } from "../home/NoFolderBanner.tsx";
import type { NoProviderCard } from "../NoProviderCard.tsx";
import type { VerifyTestNudge } from "../VerifyTestNudge.tsx";

const seams = vi.hoisted(() => ({
  chat: null as unknown as ComponentProps<typeof ChatView>, rail: null as unknown as ComponentProps<typeof ChatTabRail>,
  stack: null as unknown as ComponentProps<typeof ChatTopChromeStack>, reached: null as unknown as ComponentProps<typeof SpendCeilingReachedNudge>,
  approaching: null as unknown as ComponentProps<typeof SpendCeilingApproachingNudge>, notices: null as unknown as ComponentProps<typeof NoticeRail>,
  protection: null as unknown as ComponentProps<typeof SessionProtectionBanners>, promote: null as unknown as ComponentProps<typeof DraftPromoteBanner>,
  folder: null as unknown as ComponentProps<typeof NoFolderBanner>, provider: null as unknown as ComponentProps<typeof NoProviderCard>,
  verify: null as unknown as ComponentProps<typeof VerifyTestNudge>, limits: vi.fn(async () => ({ project: "host" })),
}));
vi.mock("../chatview/ChatView.tsx", () => ({ ChatView: (p: typeof seams.chat) => { createEffect(() => { seams.chat = { ...p }; }); return <div data-testid="chat-boundary">{p.sessionId}</div>; } }));
vi.mock("../nav/ChatTabRail.tsx", () => ({ ChatTabRail: (p: typeof seams.rail) => { createEffect(() => { seams.rail = { ...p }; }); return <>{p.dock}</>; } }));
vi.mock("../nav/ChatTopChromeStack.tsx", () => ({ ChatTopChromeStack: (p: typeof seams.stack) => {
  createEffect(() => { seams.stack = { ...p }; });
  return <For each={Object.values(p)}>{(slot) => slot.present() ? slot.children() : null}</For>;
} }));
vi.mock("../SpendCeilingReachedNudge.tsx", () => ({ spendCeilingReachedNotice: () => ({}), SpendCeilingReachedNudge: (p: typeof seams.reached) => { createEffect(() => { seams.reached = { ...p }; }); return null; } }));
vi.mock("../SpendCeilingApproachingNudge.tsx", () => ({ spendCeilingApproachingVisible: () => true, SpendCeilingApproachingNudge: (p: typeof seams.approaching) => { createEffect(() => { seams.approaching = { ...p }; }); return null; } }));
vi.mock("../NoticeRail.tsx", () => ({ NoticeRail: (p: typeof seams.notices) => { createEffect(() => { seams.notices = { ...p }; }); return null; } }));
vi.mock("./SessionProtectionBanners.tsx", () => ({ sessionProtectionPresent: () => true, SessionProtectionBanners: (p: typeof seams.protection) => { createEffect(() => { seams.protection = { ...p }; }); return null; } }));
vi.mock("../home/DraftPromoteBanner.tsx", () => ({ DraftPromoteBanner: (p: typeof seams.promote) => { createEffect(() => { seams.promote = { ...p }; }); return null; } }));
vi.mock("../home/NoFolderBanner.tsx", () => ({ NoFolderBanner: (p: typeof seams.folder) => { createEffect(() => { seams.folder = { ...p }; }); return null; } }));
vi.mock("../NoProviderCard.tsx", () => ({ NoProviderCard: (p: typeof seams.provider) => { createEffect(() => { seams.provider = { ...p }; }); return null; } }));
vi.mock("../VerifyTestNudge.tsx", () => ({ VerifyTestNudge: (p: typeof seams.verify) => { createEffect(() => { seams.verify = { ...p }; }); return null; } }));
vi.mock("../../settings/extensions/verify-test-suggestion.ts", () => ({ createVerifyTestSuggestion: () => ({ suggesting: () => true }) }));
vi.mock("../../notices/notice-select.ts", () => ({ hasSharedNotices: () => true, selectSessionNotices: () => [{ id: "notice" }] }));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => ({ getLimitsSettings: seams.limits }) }));

function mountChat() {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  appStore.actions.setCurrentSession({ id: "session", project_id: "project", status: "idle", ui: { protection: { isolation_degraded: true } } } as never);
  const shell = createShellStore("connected"); shell.commitStageScope({ projectId: "project", sessionId: "session", initialPrompt: true });
  const settingsStore = createSettingsStore();
  const [active, setActive] = createSignal(true);
  const [pending, setPending] = createSignal(false);
  const actions = { sendPrompt: vi.fn(async () => undefined), stopChat: vi.fn(async () => undefined), resumeSpendLimitedChat: vi.fn(async () => undefined) };
  const workerDrawer = { selectedWorkerId: () => "worker", workersOpen: () => true, workerDrawerFocus: () => null,
    closeWorkers: vi.fn(), acknowledgeDrawerFocus: vi.fn(() => null), openWorkers: vi.fn() };
  const callbacks = { onPromote: vi.fn(), dismissDraftPromoteBanner: vi.fn(), dismissNoFolderBanner: vi.fn(),
    addFolderToActiveProject: vi.fn(async () => undefined), onOpenBudgets: vi.fn(), onOpenProviders: vi.fn(), onOpenTests: vi.fn(), onOpenFiles: vi.fn() };
  const notices = { index: () => new Map(), dismiss: vi.fn(), dismissScope: vi.fn() };
  const showWindowNav = vi.fn(async () => true);
  render(() => <ChatWorkspace shell={shell} appStore={appStore} recents={{} as never} projects={{} as never}
    chat={{ projectId: "project", sessionId: "session" }} pending={pending} surfaceActive={active}
    settingsStore={settingsStore} costStore={{ state: { session: { estimated_nano_usd: 3e9 } } } as never} notices={notices as never}
    actions={actions as never} workerDrawer={workerDrawer} panes={{ chatHeaderLeadsWindow: () => true, windowNavCollapsed: () => true,
      navTouchesChat: () => false, navAutomaticallyCollapsed: () => false, contextHidden: () => false, navSide: () => "left",
      showWindowNav, conversationSeam: () => false } as never}
    onboarding={{ showNoProviderBanner: () => true, providerGap: () => "missing-provider" as never }}
    projectDirForChat={() => "/garden"} nav={() => "projects"} splitLive={() => true} notificationStack={() => <div>Shared notice</div>}
    showPromoteNudge={() => true} showNoFolderBanner={() => true} {...callbacks} />);
  return { appStore, settingsStore, actions, workerDrawer, callbacks, notices, setActive, setPending, showWindowNav };
}

beforeEach(() => vi.clearAllMocks());
describe("chat workspace composition", () => {
  it("preserves the chat and worker binding while visibility changes", () => {
    const { setActive, setPending, workerDrawer, actions, showWindowNav } = mountChat();
    expect(seams.chat.sessionId).toBe("session"); expect(seams.chat.projectId).toBe("project");
    expect(seams.chat.projectDir).toBe("/garden"); expect(seams.chat.hasInitialPrompt).toBe(true);
    expect(seams.chat.workersDrawerOpen).toBe(true); expect(seams.chat.workersBackgroundHydrate).toBe(true);
    expect(seams.chat.onSend).toBe(actions.sendPrompt); expect(seams.chat.onStop).toBe(actions.stopChat);
    expect(seams.chat.onOpenWorker).toBe(workerDrawer.openWorkers); expect(seams.chat.onWorkersClose).toBe(workerDrawer.closeWorkers);
    setActive(false); expect(seams.chat.workersDrawerOpen).toBe(false); expect(seams.chat.surfaceActive).toBe(false);
    setPending(true); expect(document.querySelector('.den-shell-stage--chat')?.getAttribute("data-boot")).toBe("ready");
    expect(seams.rail.navCollapsed).toBe(true); expect(seams.rail.navSplitFallback).toBe(true);
    seams.rail.onNavExpand?.(); expect(showWindowNav).toHaveBeenCalledOnce();
  });

  it("routes notices, recovery limits, and project nudges to their owning scope", async () => {
    const { notices, settingsStore, callbacks, actions } = mountChat();
    seams.notices.onDismiss?.("notice"); seams.notices.onDismissAll?.();
    expect(notices.dismiss).toHaveBeenCalledWith("notice"); expect(notices.dismissScope).toHaveBeenCalledWith(expect.objectContaining({ projectId: "project", sessionId: "session" }));
    await seams.reached.onResume?.(); expect(actions.resumeSpendLimitedChat).toHaveBeenCalledWith({ projectId: "project", sessionId: "session" });
    seams.reached.onOpenBudgets?.(); expect(callbacks.onOpenBudgets).toHaveBeenCalledOnce();
    const projectLimits = { max_session_usd: 12 } as never;
    seams.reached.onLimitsUpdated?.({ scope: "project", limits: projectLimits });
    expect(settingsStore.state.effectiveLimits?.projectId).toBe("project");
    expect(settingsStore.state.effectiveLimits?.limits).toEqual(projectLimits);
    seams.approaching.onLimitsUpdated?.({ scope: "global", limits: projectLimits });
    await Promise.resolve();
    expect(settingsStore.state.limits).toEqual(projectLimits); expect(seams.limits).toHaveBeenCalledWith("project");
    seams.promote.onPromote(); seams.promote.onDismiss(); seams.folder.onAddFolder(); seams.folder.onDismiss();
    expect(callbacks.onPromote).toHaveBeenCalledWith("project"); expect(callbacks.dismissDraftPromoteBanner).toHaveBeenCalledWith("project");
    expect(callbacks.dismissNoFolderBanner).toHaveBeenCalledWith("project"); expect(callbacks.addFolderToActiveProject).toHaveBeenCalledOnce();
    seams.provider.onOpenProviders(); seams.verify.onOpenSettings?.(); seams.provider.onDismiss?.();
    expect(callbacks.onOpenProviders).toHaveBeenCalledOnce(); expect(callbacks.onOpenTests).toHaveBeenCalledOnce();
    expect(seams.stack.provider?.present()).toBe(false);
    seams.protection.onDismiss?.("isolation_degraded" as never);
  });
});
