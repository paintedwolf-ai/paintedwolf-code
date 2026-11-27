import { afterEach, expect, it, vi } from "vitest";
import { applyContinuationNotice, resetContinuationNotices } from "./continuation-notices.ts";
import { noticeAction, registerSessionPromptActions, resetNoticeActionSinksForTest } from "./notice-actions.ts";
import { selectSessionNotices } from "./notice-select.ts";
import { createNoticeStore, registerNoticePublisher } from "./notice-store.ts";

afterEach(() => {
  resetContinuationNotices();
  resetNoticeActionSinksForTest();
  registerNoticePublisher(null);
});

it("reconciles recovery actions across event delivery and snapshot reload", () => {
  const store = createNoticeStore();
  registerNoticePublisher(store);
  const keepGoing = vi.fn();
  registerSessionPromptActions("s", { keepGoing, retry: vi.fn(), rewindAndRetry: vi.fn() });
  const failure = {
    code: "coordinator_not_continued",
    title: "Coordinator did not continue",
    message: "The coordinator needs a request.",
    actions: ["prompt_keep_going"],
  } as const;
  function publish(revision: number, id: string) {
    applyContinuationNotice("p", {
      session_id: "s", revision, continuation_id: id,
      host_error: { ...failure, actions: [...failure.actions], message: id },
    });
  }
  publish(1, "a");
  publish(2, "b");
  applyContinuationNotice("p", { session_id: "s", revision: 1 });
  publish(2, "b");
  const notices = selectSessionNotices(store.index(), "s");
  expect(notices).toHaveLength(1);
  noticeAction(notices[0]!)?.run();
  expect(keepGoing).toHaveBeenCalledWith("b");
  store.clearSessionHostErrors("s");
  expect(selectSessionNotices(store.index(), "s")).toHaveLength(1);
  applyContinuationNotice("p", { session_id: "s", revision: 3 });
  publish(2, "b");
  expect(selectSessionNotices(store.index(), "s")).toHaveLength(0);
});
