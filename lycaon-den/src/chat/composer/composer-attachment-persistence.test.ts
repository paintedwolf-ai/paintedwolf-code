// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { COMPOSER_DRAFTS_SESSION_CAP } from "../../../shared/app-state-types.ts";
import { loadAppState } from "../../platform/persistence/app-state.ts";
import { flushAppState, getAppStateSnapshot, resetAppStateSnapshotForTests, setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { parseComposerAttachmentDrafts, retainedComposerAttachment } from "./composer-attachment-persistence.ts";
import { acquireSharedComposerDocumentLease, applySharedComposerMutation, clearSharedComposerDocument, removeSharedComposerAttachment, resetSharedComposerDocumentsForTests, resolveSharedComposerDocument } from "./shared-composer-document.ts";

const destination = { projectId: "project-1", sessionId: "session-1" };
const seed = { ...destination, draft: "Read the attachment", attachments: [] };
const attachment = { id: "attachment-1", kind: "text" as const, name: "README.md", mime: "text/plain", blobId: "a".repeat(64), byteLength: 80 };

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  resetAppStateSnapshotForTests();
  resetSharedComposerDocumentsForTests();
});
afterEach(() => {
  resetSharedComposerDocumentsForTests();
  resetAppStateSnapshotForTests();
  vi.useRealTimers();
});

async function restart() {
  await flushAppState();
  const saved = await loadAppState();
  resetSharedComposerDocumentsForTests();
  resetAppStateSnapshotForTests();
  setAppStateSnapshot(saved);
}

describe("staged composer restart retention", () => {
  it.each(["resolve", "acquire"])("restores an uploaded attachment before %s", async (entry) => {
    await applySharedComposerMutation(seed, [attachment]);
    await restart();
    const restored = entry === "resolve"
      ? await resolveSharedComposerDocument(seed)
      : await acquireSharedComposerDocumentLease(seed);
    expect(restored.attachments).toEqual([attachment]);
    expect(restored.draft).toBe(seed.draft);
    await clearSharedComposerDocument(destination, true);
    await restart();
    expect((await resolveSharedComposerDocument({ ...seed, draft: "" })).attachments).toEqual([]);
  });

  it("retains removal across restart without affecting another chat", async () => {
    await applySharedComposerMutation(seed, [attachment]);
    const other = { ...seed, sessionId: "session-2" };
    await applySharedComposerMutation(other, [{ ...attachment, id: "attachment-2" }]);
    await removeSharedComposerAttachment(destination, attachment.id);
    await restart();
    expect((await resolveSharedComposerDocument(seed)).attachments).toEqual([]);
    expect((await resolveSharedComposerDocument(other)).attachments).toHaveLength(1);
  });

  it("cannot restore a blob into a different project", async () => {
    await applySharedComposerMutation(seed, [attachment]);
    await restart();
    expect((await resolveSharedComposerDocument({ ...seed, projectId: "project-2" })).attachments).toEqual([]);
  });

  it("stores only reference metadata, never bodies, previews, or secret values", async () => {
    await applySharedComposerMutation(seed, [{ ...attachment, preview: "PRIVATE BODY", previewUrl: "blob:temporary" }]);
    const encoded = JSON.stringify(getAppStateSnapshot().composerAttachments);
    expect(encoded).not.toContain("PRIVATE BODY");
    expect(encoded).not.toContain("blob:temporary");
    const secret = retainedComposerAttachment({ id: "secret-1", kind: "secret", name: "Token", projectId: "project-1",
      reference: "{{paintedwolf-secret:11111111-1111-4111-8111-111111111111}}", scope: "chat", runeLength: 12,
      value: "PRIVATE SECRET", plaintext: "PRIVATE SECRET", preview: "PRIVATE SECRET" }, "project-1");
    expect(secret?.kind).toBe("secret");
    expect(JSON.stringify(secret)).not.toContain("PRIVATE SECRET");
    expect(retainedComposerAttachment({ ...secret, reference: "PRIVATE SECRET" }, "project-1")).toBeUndefined();
  });

  it("bounds untrusted saved metadata and rejects incomplete or foreign references", () => {
    const drafts = Object.fromEntries(Array.from({ length: COMPOSER_DRAFTS_SESSION_CAP + 3 }, (_, i) => [String(i), {
      projectId: "project-1", attachments: Array.from({ length: 140 }, () => attachment),
    }]));
    const parsed = parseComposerAttachmentDrafts(drafts);
    expect(Object.keys(parsed)).toHaveLength(COMPOSER_DRAFTS_SESSION_CAP);
    expect(Object.values(parsed).every((draft) => draft.attachments.length === 128)).toBe(true);
    for (const invalid of [null, {}, { ...attachment, blobId: undefined }, { ...attachment, byteLength: -1 },
      { id: "path", name: "source", kind: "path-file", projectId: "project-2", rootId: "root", path: "README.md" }]) {
      expect(retainedComposerAttachment(invalid, "project-1")).toBeUndefined();
    }
  });
});
