import { stubClient } from "./client-fixture.ts";
import { type ComponentProps } from "solid-js";
import { afterEach, beforeEach, expect, vi } from "vitest";
import { fireEvent, waitFor } from "@solidjs/testing-library";
import { Composer as ComposerView } from "../components/chatview/Composer.tsx";
import { resetComposerDraftsForTests } from "../chat/composer/composer-drafts.ts";
import { resetComposerAttachmentsForTests } from "../chat/composer/composer-attachment-store.ts";
import { resetPromptRecallForTests } from "../chat/composer/prompt-recall.ts";
import { resetComposerDocumentStoreForTests } from "../chat/composer/composer-document-store.ts";
import { resetComposerAttachmentSinkForTests } from "../chat/composer/add-to-chat.ts";
import { resetDispatcherForTests } from "../shortcuts/dispatcher.ts";
import { setLycaonClientForTest } from "../platform/connection/app-connection.ts";
import type { AttachmentUploadResponse } from "../api/types.ts";
import { mockAttachmentCapabilities } from "../api/mocks/fixtures.ts";
import { setPreflightReport } from "../platform/persistence/preflight-report.ts";
import { resetAppStateSnapshotForTests } from "../store/app-state-snapshot.ts";

function seedUploadHost(): void {
  setLycaonClientForTest(stubClient({
    uploadAttachment: async (_projectId: string, filename: string): Promise<AttachmentUploadResponse> => {
      if (filename.endsWith(".bin")) {
        throw Object.assign(new Error("unsupported"), { code: "unsupported_attachment" });
      }
      return {
        blob_id: "a".repeat(64),
        filename,
        mime: filename.endsWith(".png") ? "image/png" : "text/plain",
        kind: filename.endsWith(".png") ? "image" : "text",
        bytes: 13,
      };
    },
  }));
}

type ComposerViewProps = ComponentProps<typeof ComposerView>;

export function Composer(
  props: Omit<ComposerViewProps, "projectId" | "sessionId"> & {
    projectId?: string;
    sessionId?: string;
  },
) {
  return (
    <ComposerView
      {...props}
      projectId={props.projectId ?? "proj-1"}
      sessionId={props.sessionId ?? "sess-1"}
    />
  );
}

vi.mock("../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../platform/runtime.ts")>()),
  isTauriRuntime: () => false,
}));

beforeEach(() => {
  resetAppStateSnapshotForTests();
  seedUploadHost();
  setPreflightReport({
    overall: "ok",
    probes: [],
    attachment_capabilities: mockAttachmentCapabilities,
  });
  resetComposerDraftsForTests();
  resetComposerAttachmentsForTests();
  resetComposerAttachmentSinkForTests();
  resetComposerDocumentStoreForTests();
  resetPromptRecallForTests();
  resetDispatcherForTests();
});

afterEach(() => {
  resetDispatcherForTests();
});

export function pressComposerKey(
  input: HTMLTextAreaElement,
  init: KeyboardEventInit,
): KeyboardEvent {
  fireEvent.focus(input);
  const event = new KeyboardEvent("keydown", {
    bubbles: true,
    cancelable: true,
    ...init,
  });
  input.dispatchEvent(event);
  return event;
}

export function typeDraft(
  input: HTMLTextAreaElement,
  value: string,
  caret = value.length,
): void {
  fireEvent.input(input, { target: { value } });
  input.setSelectionRange(caret, caret);
}

export async function sendDraft(
  input: HTMLTextAreaElement,
  value: string,
): Promise<void> {
  typeDraft(input, value);
  pressComposerKey(input, { key: "Enter", code: "Enter" });
  await waitFor(() => expect(input.value).toBe(""));
}
