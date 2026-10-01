import { afterEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { hasSpendRecoveryDraft, resumeAfterSpendCeiling } from "./spend-ceiling-recovery.ts";
import { composerDraftForSession, resetComposerDraftsForTests, setComposerDraft } from "../composer/composer-drafts.ts";
import { pendingAttachmentsForSession, replacePendingAttachmentsForSession, resetComposerAttachmentsForTests } from "../composer/composer-attachment-store.ts";

afterEach(() => {
  resetComposerDraftsForTests();
  resetComposerAttachmentsForTests();
});

describe("spend ceiling recovery", () => {
  it.each(["text", "attachment", "both"])("preserves unsent %s without continuing earlier work", async (kind) => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "session", project_id: "project", status: "idle" } as never);
    if (kind !== "attachment") setComposerDraft("session", "New request after a rejected send");
    if (kind !== "text") replacePendingAttachmentsForSession("session", [{ id: "fixture", kind: "path-file", label: "fixture.txt" } as never]);
    const before = pendingAttachmentsForSession("session");
    const draft = composerDraftForSession("session");
    const send = vi.fn();
    expect(hasSpendRecoveryDraft("session")).toBe(true);
    expect(await resumeAfterSpendCeiling(store, "session", send)).toBe(true);
    expect(send).not.toHaveBeenCalled();
    expect(composerDraftForSession("session")).toBe(draft);
    expect(pendingAttachmentsForSession("session")).toEqual(before);
  });

  it("ignores another session's unsent content", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "session", project_id: "project", status: "idle" } as never);
    setComposerDraft("other", "Keep this request");
    replacePendingAttachmentsForSession("other", [{ id: "fixture", kind: "path-file" } as never]);
    const send = vi.fn().mockResolvedValue(undefined);
    expect(hasSpendRecoveryDraft("session")).toBe(false);
    expect(await resumeAfterSpendCeiling(store, "session", send)).toBe(true);
    expect(send).toHaveBeenCalledOnce();
  });
  it.each(["/plan Add a ready endpoint", "/security Review this project", "Publish the selected artifact"])(
    "continues existing work without replaying %s",
    async (previousAsk) => {
      const store = createAppStore();
      store.actions.setCurrentSession({ id: "session", project_id: "project", status: "idle" } as never);
      store.actions.installTranscriptBaseline("session", [{ id: "ask", role: "user", content: previousAsk, ts: "2026-09-12T00:00:00Z", ord: 1 }] as never, 1);
      const send = vi.fn().mockResolvedValue(undefined);
      expect(await resumeAfterSpendCeiling(store, "session", send)).toBe(true);
      expect(send).toHaveBeenCalledOnce();
      const prompt = send.mock.calls[0]![0] as string;
      expect(prompt.startsWith("/")).toBe(false);
      expect(prompt).not.toContain(previousAsk);
      expect(prompt).toContain("Continue the current work");
    },
  );

  it.each(["changed", "busy"])("does not send into a %s session after raising the ceiling", async (state) => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: state === "changed" ? "other" : "session", project_id: "project", status: state === "busy" ? "busy" : "idle" } as never);
    const send = vi.fn().mockResolvedValue(undefined);
    expect(await resumeAfterSpendCeiling(store, "session", send)).toBe(false);
    expect(send).not.toHaveBeenCalled();
  });

  it("reports a refused send without treating it as resumed", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "session", project_id: "project", status: "idle" } as never);
    expect(await resumeAfterSpendCeiling(store, "session", async () => false)).toBe(false);
  });
});
