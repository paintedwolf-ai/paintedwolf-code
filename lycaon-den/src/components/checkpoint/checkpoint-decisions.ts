import { createCheckpointSubmission } from "./checkpoint-submission.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ContentReviewRule } from "../../api/types.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { resolveContentApply, resolveToolApproval } from "../../chat/checkpoint/checkpoint-actions.ts";
import { composerDraftForSession, flushComposerDraftsToDisk } from "../../chat/composer/composer-drafts.ts";
import { pendingAttachmentsForSession } from "../../chat/composer/composer-attachment-store.ts";
import { promptPartsFromPendingAttachments } from "../../chat/composer/prompt-parts.ts";
import { removeComposerDocumentAttachment, replaceComposerDocumentDraft } from "../../chat/composer/composer-document-store.ts";

/** Review rules match an exact path or a directory subtree. */
function reviewRuleMatches(
  rule: ContentReviewRule,
  tool: string,
  path: string,
): boolean {
  if (rule.tool && rule.tool !== tool) return false;
  const p = rule.path.trim();
  if (p === "" || p === "*" || p === "**") return true;
  if (p.endsWith("/**")) {
    const prefix = p.slice(0, -3);
    return path === prefix || path.startsWith(prefix + "/");
  }
  return p === path;
}

type Dependencies = {
  client: LycaonClient;
  sessionId: () => string;
  projectId: () => string | undefined;
  onStart: (checkpointId: string) => void;
  reportError: (error: unknown) => void;
};

/** Submits decisions and consumes their captured guidance after commitment. */
export function createCheckpointDecisions(dependencies: Dependencies) {
  const submissions = createCheckpointSubmission();
  const scope = (checkpoint: PendingCheckpoint, projectId?: string) => sessionScope(projectId ?? "", checkpoint.sessionId);

  const captureGuidance = (sessionId: string, withGuidance: boolean) => {
    if (!withGuidance) return { draft: "", secrets: [], secretAttachmentIds: [] };
    const attachments = pendingAttachmentsForSession(sessionId).filter((attachment) => attachment.kind === "secret");
    return {
      draft: composerDraftForSession(sessionId),
      secrets: promptPartsFromPendingAttachments(attachments).secrets,
      secretAttachmentIds: attachments.map((attachment) => attachment.id),
    };
  };
  type Guidance = ReturnType<typeof captureGuidance>;

  const consumeGuidance = async (captured: Guidance, sessionId: string, projectId?: string) => {
    if (!projectId || (!captured.draft.trim() && captured.secretAttachmentIds.length === 0)) return;
    if (composerDraftForSession(sessionId) !== captured.draft) return;
    try {
      await replaceComposerDocumentDraft({ projectId, sessionId }, "");
      for (const attachmentId of captured.secretAttachmentIds) {
        await removeComposerDocumentAttachment({ projectId, sessionId }, attachmentId);
      }
      await flushComposerDraftsToDisk();
    } catch (error) {
      dependencies.reportError(error);
    }
  };

  const submit = async (checkpoint: PendingCheckpoint, withGuidance: boolean, answer: (captured: Guidance) => Promise<void>) => {
    if (submissions.pending(checkpoint.checkpointId)) return;
    const sessionId = dependencies.sessionId();
    const projectId = dependencies.projectId();
    let captured: Guidance | undefined;
    dependencies.onStart(checkpoint.checkpointId);
    const completed = await submissions.run(checkpoint.checkpointId, scope(checkpoint, projectId), async () => {
      captured = captureGuidance(sessionId, withGuidance);
      await answer(captured);
    });
    if (completed && captured) await consumeGuidance(captured, sessionId, projectId);
  };

  const onToolApproval = (checkpoint: PendingCheckpoint, action: "approve" | "reject", options?: { optionId?: string; withGuidance?: boolean }) =>
    submit(checkpoint, action === "reject" && Boolean(options?.withGuidance), (captured) =>
      resolveToolApproval(dependencies.client, checkpoint, action, {
        optionId: options?.optionId,
        guidance: captured.draft.trim() || undefined,
        secrets: captured.secrets,
      }),
    );

  const onContentApply = (checkpoint: PendingCheckpoint, body: { decision: "approve" | "reject" | "approve_partial"; approvedHunks?: string[]; withGuidance?: boolean }) =>
    submit(checkpoint, body.decision === "reject" && Boolean(body.withGuidance), (captured) =>
      resolveContentApply(dependencies.client, checkpoint, body.decision, {
        approvedHunks: body.approvedHunks,
        guidance: captured.draft.trim() || undefined,
        secrets: captured.secrets,
      }),
    );

  const skipReviewPath = async (checkpoint: PendingCheckpoint, mode: "day" | "always") => {
    if (submissions.pending(checkpoint.checkpointId)) return;
    const path = checkpoint.content_apply?.path?.trim();
    const tool = checkpoint.content_apply?.tool?.trim() || "write";
    const projectId = dependencies.projectId();
    if (!path || !projectId) return;
    dependencies.onStart(checkpoint.checkpointId);
    await submissions.run(checkpoint.checkpointId, scope(checkpoint, projectId), async () => {
      const current = await dependencies.client.getReviewSettings(projectId);
      const review_paths = mode === "always"
        ? (current.review_paths ?? []).filter((rule) => !reviewRuleMatches(rule, tool, path))
        : (current.review_paths ?? []).map((rule) => reviewRuleMatches(rule, tool, path)
          ? { ...rule, disabled_until: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString() }
          : rule);
      await dependencies.client.updateReviewSettings({ review_paths }, projectId);
      await resolveContentApply(dependencies.client, checkpoint, "approve");
    });
  };

  return { ...submissions, onToolApproval, onContentApply, skipReviewPath };
}
