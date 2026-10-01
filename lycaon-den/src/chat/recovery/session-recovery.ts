import type { LycaonClient } from "../../api/client.ts";
import type { Message, RewindSessionResponse } from "../../api/types.ts";
import type { Project } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import {
  clearComposerDocumentAttachments,
  replaceComposerDocumentDraft,
  stageComposerMutation,
} from "../composer/composer-document-store.ts";
import { pendingAttachmentsFromRestore } from "../transcript/content/restore-attachments.ts";
import { applySessionTranscriptSnapshot } from "../session/session-transcript-hydrate.ts";

/** Recovery actions rewind an ask and restore its composer draft. */
export type RecoveryAction = "edit" | "rewind";

export type RecoveryTarget = {
  /** Reused when a response or transcript refresh is interrupted. */
  operationId: string;
  action: RecoveryAction;
  messageId: string;
  /** Prompt text restored into the composer. */
  text: string;
};

/** Rewind anchors are visible user messages. */
export function isRewindAnchor(msg: Message): boolean {
  return msg.role === "user" && msg.visibility !== "internal"
    && msg.kind !== "workflow_boundary" && msg.kind !== "user_continuation"
    && !msg.workflow_boundary;
}

/** Return the newest rewind anchor. */
export function lastRewindAnchorId(
  messages: readonly Message[],
): string | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    if (msg && isRewindAnchor(msg)) return msg.id;
  }
  return null;
}

export type RunRewindDeps = {
  client: LycaonClient;
  appStore: AppStore;
  projectId: string;
  sessionId: string;
  projectDir: string;
  projects: readonly Project[];
};

/** Rewind on the host, then reload transcript and composer state. */
export async function runRewind(
  deps: RunRewindDeps,
  target: RecoveryTarget,
  planDigest: string,
): Promise<RewindSessionResponse> {
  const result = await deps.client.rewindSession(deps.sessionId, {
    operation_id: target.operationId,
    message_id: target.messageId,
    mode: "before_turn",
    plan_digest: planDigest,
  });

  // Pending sends cannot outlive transcript rewind.
  const lingering = deps.appStore.state.pendingSends[deps.sessionId];
  if (lingering?.length) {
    deps.appStore.actions.removePendingSends(
      deps.sessionId,
      lingering.map((entry) => entry.operationId),
    );
  }

  const transcript = await deps.client.listSessionMessages(deps.sessionId);
  await applySessionTranscriptSnapshot(
    deps.appStore,
    deps.client,
    deps.sessionId,
    deps.projectDir,
    transcript,
    deps.projects,
  );

  const destination = {
    projectId: deps.projectId,
    sessionId: deps.sessionId,
  };
  await replaceComposerDocumentDraft(
    destination,
    result.restored_prompt ?? target.text,
  );
  await clearComposerDocumentAttachments(destination);
  const project = deps.projects.find((p) => p.id === deps.projectId);
  const pending = pendingAttachmentsFromRestore({
    projectId: deps.projectId,
    sessionId: deps.sessionId,
    project,
    contentParts: result.restored_content_parts,
    artifactIds: result.restored_artifact_ids,
  });
  if (pending.length > 0) {
    await stageComposerMutation(destination, pending);
  }
  return result;
}
