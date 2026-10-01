import { afterEach, describe, expect, it, vi } from "vitest";
import {
  noticeAction,
  noticeActions,
  registerSessionPromptActions,
  resetNoticeActionSinksForTest,
  setShellNoticeActionSinks,
} from "./notice-actions.ts";
import type { AppNotice } from "./notice-model.ts";
import { APP_SCOPE, projectScope, sessionScope, type NoticeScope } from "./notice-scope.ts";

const notice = (...actions: string[]): AppNotice => ({
  id: "n1",
  severity: "error",
  title: "No model configured",
  message: "This session needs a language model before it can respond.",
  suggestedAction: "Open Settings → AI providers and assign a provider.",
  createdAt: 0,
  actions: actions.length > 0 ? actions : undefined,
  scope: APP_SCOPE,
});

const promptHandlers = () => ({
  retry: vi.fn(),
  keepGoing: vi.fn(),
  rewindAndRetry: vi.fn(),
});

const scoped = (scope: NoticeScope, ...actions: string[]) => ({ actions, scope });

afterEach(() => resetNoticeActionSinksForTest());

describe("noticeAction", () => {
  it("makes the provider notice actionable", () => {
    const open = vi.fn();
    setShellNoticeActionSinks({ openAIProviders: open });
    const action = noticeAction(notice("open_ai_providers"));
    expect(action?.label).toBe("Open AI providers");
    action?.run();
    expect(open).toHaveBeenCalledOnce();
  });

  it("leaves unrelated notices without an action", () => {
    setShellNoticeActionSinks({ openAIProviders: vi.fn() });
    expect(noticeAction(notice("not_an_action"))).toBeNull();
    expect(noticeAction(notice())).toBeNull();
  });

  it("offers no action before the shell wires navigation", () => {
    expect(noticeAction(notice("open_ai_providers"))).toBeNull();
  });

  it("resolves prompt recovery actions with proper labels and variants", () => {
    const h = promptHandlers();
    registerSessionPromptActions("s1", h);
    const scope = sessionScope("p1", "s1");

    const actRetry = noticeAction(scoped(scope, "prompt_retry"));
    expect(actRetry?.label).toBe("Retry");
    expect(actRetry?.variant).toBe("primary");
    actRetry?.run();
    expect(h.retry).toHaveBeenCalledOnce();

    const actKeep = noticeAction(scoped(scope, "prompt_keep_going"));
    expect(actKeep?.label).toBe("Keep going");
    expect(actKeep?.variant).toBe("primary");
    actKeep?.run();
    expect(h.keepGoing).toHaveBeenCalledOnce();

    const actRewind = noticeAction(scoped(scope, "prompt_rewind_and_retry"));
    expect(actRewind?.label).toBe("Rewind and retry");
    expect(actRewind?.variant).toBe("secondary");
    actRewind?.run();
    expect(h.rewindAndRetry).toHaveBeenCalledOnce();
  });

  it("resolves multiple actions via noticeActions", () => {
    registerSessionPromptActions("s1", promptHandlers());
    setShellNoticeActionSinks({ openAIProviders: vi.fn() });

    const actions = noticeActions(
      scoped(
        sessionScope("p1", "s1"),
        "prompt_keep_going",
        "prompt_rewind_and_retry",
        "open_ai_providers",
      ),
    );
    expect(actions.map((a) => a.id)).toEqual([
      "prompt_keep_going",
      "prompt_rewind_and_retry",
      "open_ai_providers",
    ]);
  });
});

describe("session prompt actions", () => {
  it("runs the handler of the notice's own chat, not the last mounted one", () => {
    const first = promptHandlers();
    const second = promptHandlers();
    registerSessionPromptActions("s1", first);
    registerSessionPromptActions("s2", second);

    noticeAction(scoped(sessionScope("p1", "s1"), "prompt_retry"))?.run();

    expect(first.retry).toHaveBeenCalledOnce();
    expect(second.retry).not.toHaveBeenCalled();
  });

  it("offers nothing when the notice's chat is not mounted", () => {
    registerSessionPromptActions("s2", promptHandlers());
    expect(noticeAction(scoped(sessionScope("p1", "s1"), "prompt_retry"))).toBeNull();
  });

  it("offers nothing for a notice without a session scope", () => {
    registerSessionPromptActions("s1", promptHandlers());
    expect(noticeAction(scoped(APP_SCOPE, "prompt_retry"))).toBeNull();
    expect(noticeAction(scoped(projectScope("p1"), "prompt_keep_going"))).toBeNull();
    expect(noticeAction({ actions: ["prompt_retry"] })).toBeNull();
  });

  it("unmounting one chat leaves other chats' handlers in place", () => {
    const first = promptHandlers();
    registerSessionPromptActions("s1", first);
    const unregisterSecond = registerSessionPromptActions("s2", promptHandlers());
    unregisterSecond();

    expect(noticeAction(scoped(sessionScope("p1", "s2"), "prompt_retry"))).toBeNull();
    noticeAction(scoped(sessionScope("p1", "s1"), "prompt_retry"))?.run();
    expect(first.retry).toHaveBeenCalledOnce();
  });

  it("an overlapping remount keeps the surviving registration", () => {
    const older = promptHandlers();
    const newer = promptHandlers();
    const unregisterOlder = registerSessionPromptActions("s1", older);
    registerSessionPromptActions("s1", newer);
    unregisterOlder();

    noticeAction(scoped(sessionScope("p1", "s1"), "prompt_retry"))?.run();
    expect(newer.retry).toHaveBeenCalledOnce();
    expect(older.retry).not.toHaveBeenCalled();
  });

  it("shell navigation survives chat registration and vice versa", () => {
    const open = vi.fn();
    setShellNoticeActionSinks({ openAIProviders: open });
    const unregister = registerSessionPromptActions("s1", promptHandlers());
    unregister();
    noticeAction(notice("open_ai_providers"))?.run();
    expect(open).toHaveBeenCalledOnce();
  });
});
