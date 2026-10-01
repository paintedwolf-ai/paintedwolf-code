// @vitest-environment jsdom
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  pendingAttachmentsForSession,
  resetComposerAttachmentsForTests,
} from "./composer-attachment-store.ts";
import {
  composerDraftForSession,
  resetComposerDraftsForTests,
} from "./composer-drafts.ts";
import {
  acquireComposerDocumentLease,
  consumeComposerDocument,
  ensureComposerDocument,
  flushComposerDocumentsToDisk,
  resetComposerDocumentStoreForTests,
  stageComposerMutation,
  updateComposerDocumentDraft,
} from "./composer-document-store.ts";

import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import type { AttachmentUploadResponse } from "../../api/types.ts";
import { loadAppState } from "../../platform/persistence/app-state.ts";
import { sharedComposerDocument } from "./shared-composer-document.ts";

/** Accepts uploads while these tests exercise staging. */
function seedUploadHost(): void {
  setLycaonClientForTest(stubClient({
    uploadAttachment: async (_projectId: string, filename: string): Promise<AttachmentUploadResponse> => ({
      blob_id: "a".repeat(64),
      filename,
      mime: "text/plain",
      kind: "text",
      bytes: 16,
    }),
  }));
}


const sharedComposerBehavior = vi.hoisted(() => ({ failPublish: false }));

vi.mock("./shared-composer-document.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./shared-composer-document.ts")>();
  return {
    ...actual,
    publishSharedComposerDraft: (...args: Parameters<typeof actual.publishSharedComposerDraft>) =>
      sharedComposerBehavior.failPublish
        ? Promise.reject(new Error("publish failed"))
        : actual.publishSharedComposerDraft(...args),
  };
});

vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  isTauriRuntime: () => false,
}));

beforeEach(() => {
  localStorage.clear();
  resetAppStateSnapshotForTests();
  seedUploadHost();
  sharedComposerBehavior.failPublish = false;
  resetComposerAttachmentsForTests();
  resetComposerDraftsForTests();
  resetComposerDocumentStoreForTests();
});

afterEach(() => vi.useRealTimers());

describe("shared composer mutation boundary", () => {
  it.each([0, 119, 299])("preserves every pending draft when closing after %i ms", async (delay) => {
    vi.useFakeTimers();
    const destinations = [
      { projectId: "project-1", sessionId: "session-a" },
      { projectId: "project-2", sessionId: "session-b" },
    ] as const;
    for (const destination of destinations) {
      await ensureComposerDocument(destination);
      await acquireComposerDocumentLease(destination);
      updateComposerDocumentDraft(destination, `Draft ${destination.sessionId} — café 🌱`);
    }
    await vi.advanceTimersByTimeAsync(delay);
    await flushComposerDocumentsToDisk();
    const restored = await loadAppState();
    for (const destination of destinations) {
      const expected = `Draft ${destination.sessionId} — café 🌱`;
      expect(restored.composerDrafts?.[destination.sessionId]).toBe(expected);
      expect(sharedComposerDocument(destination)?.draft).toBe(expected);
    }
    updateComposerDocumentDraft(destinations[0], "");
    await flushComposerDocumentsToDisk();
    expect((await loadAppState()).composerDrafts?.["session-a"]).toBeUndefined();
    expect(sharedComposerDocument(destinations[0])?.draft).toBe("");
  });

  it("blocks close on an unacknowledged draft and permits retry without losing local text", async () => {
    vi.useFakeTimers();
    const destination = { projectId: "project-1", sessionId: "session-2" };
    await ensureComposerDocument(destination);
    await acquireComposerDocumentLease(destination);
    updateComposerDocumentDraft(destination, "Keep this locally");
    sharedComposerBehavior.failPublish = true;
    await expect(flushComposerDocumentsToDisk()).rejects.toThrow("Could not preserve");
    expect((await loadAppState()).composerDrafts?.["session-2"]).toBe("Keep this locally");
    sharedComposerBehavior.failPublish = false;
    await flushComposerDocumentsToDisk();
    expect(sharedComposerDocument(destination)?.draft).toBe("Keep this locally");
  });

  it("keeps typing that starts while the document resolves", async () => {
    const destination = { projectId: "project-1", sessionId: "session-2" };
    const resolving = ensureComposerDocument(destination);
    updateComposerDocumentDraft(destination, "Started immediately");

    await resolving;

    expect(composerDraftForSession("session-2")).toBe("Started immediately");
  });

  it("stages an attachment and prefill in the same inactive chat", async () => {
    const result = await stageComposerMutation(
      { projectId: "project-1", sessionId: "session-2" },
      [
        {
          id: "attachment-1",
          kind: "path-file",
          projectId: "project-1",
          rootId: "root-1",
          path: "src/main.ts",
          name: "main.ts",
        },
      ],
      "Explain this.",
    );

    expect(result).toEqual({ ok: true });
    expect(pendingAttachmentsForSession("session-2")).toHaveLength(1);
    expect(composerDraftForSession("session-2")).toBe("Explain this.");
  });

  it("rejects a reference whose project differs from the destination", async () => {
    const result = await stageComposerMutation(
      { projectId: "project-2", sessionId: "session-2" },
      [
        {
          id: "attachment-1",
          kind: "artifact",
          projectId: "project-1",
          artifactId: "artifact-1",
          name: "Result",
        },
      ],
    );

    expect(result).toEqual({
      ok: false,
      reason: "Choose a chat in the same project as this reference.",
    });
    expect(pendingAttachmentsForSession("session-2")).toHaveLength(0);
  });

  it("does not let an attachment revision erase a pending editor draft", async () => {
    vi.useFakeTimers();
    const destination = { projectId: "project-1", sessionId: "session-2" };
    await ensureComposerDocument(destination);
    const leased = await acquireComposerDocumentLease(destination);
    expect(leased.leaseClientId).toBe("main");
    updateComposerDocumentDraft(destination, "Still typing");

    await stageComposerMutation(destination, [
      {
        id: "attachment-1",
        kind: "path-file",
        projectId: "project-1",
        rootId: "root-1",
        path: "README.md",
        name: "README.md",
      },
    ]);

    expect(composerDraftForSession("session-2")).toBe("Still typing");
    await vi.runAllTimersAsync();
    expect(composerDraftForSession("session-2")).toBe("Still typing");
  });

  it("keeps the local draft after transient publish failures", async () => {
    vi.useFakeTimers();
    const destination = { projectId: "project-1", sessionId: "session-2" };
    await ensureComposerDocument(destination);
    await acquireComposerDocumentLease(destination);
    sharedComposerBehavior.failPublish = true;

    updateComposerDocumentDraft(destination, "Keep this locally");
    await vi.runAllTimersAsync();

    expect(composerDraftForSession("session-2")).toBe("Keep this locally");
  });

  it("cancels a delayed draft publish when send consumes the document", async () => {
    vi.useFakeTimers();
    const destination = { projectId: "project-1", sessionId: "session-2" };
    await ensureComposerDocument(destination);
    await acquireComposerDocumentLease(destination);
    updateComposerDocumentDraft(destination, "Already sent");

    await consumeComposerDocument(destination);
    await vi.runAllTimersAsync();

    expect(composerDraftForSession("session-2")).toBe("");
  });
});
