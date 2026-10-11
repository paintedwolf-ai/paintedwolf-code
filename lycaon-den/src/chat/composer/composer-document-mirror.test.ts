import { expect, it, vi } from "vitest";
import { startComposerDocumentMirror } from "./composer-document-store.ts";
import { composerDraftForSession } from "./composer-drafts.ts";
import type { SharedComposerDocument } from "./shared-composer-document.ts";

const listeners = vi.hoisted(() => new Set<(document: SharedComposerDocument) => void>());
vi.mock("./shared-composer-document.ts", async (original) => ({
  ...await original<typeof import("./shared-composer-document.ts")>(),
  subscribeSharedComposerDocuments: (listener: (document: SharedComposerDocument) => void) => {
    listeners.add(listener);
    return () => { listeners.delete(listener); };
  },
}));

it("keeps the replacement mirror subscribed after an older disposer runs", () => {
  const first = startComposerDocumentMirror();
  const second = startComposerDocumentMirror();
  first();
  expect(listeners.size).toBe(1);
  for (const listener of listeners) listener({ projectId: "project", sessionId: "mirror", draft: "live", attachments: [], revision: 1 });
  expect(composerDraftForSession("mirror")).toBe("live");
  second();
  second();
  expect(listeners.size).toBe(0);
});
