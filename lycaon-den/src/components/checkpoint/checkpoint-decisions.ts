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
  sessionId: string;
  projectId: () => string | undefined;
  onStart: (checkpointId: string) => void;
  reportError: (error: unknown) => void;
};

/** Submits checkpoint decisions and consumes guidance only after commitment. */
export function createCheckpointDecisions(dependencies: Dependencies) {
  const submissions = createCheckpointSubmission();
  const scope = (checkpoint: PendingCheckpoint) => sessionScope(dependencies.projectId() ?? "", checkpoint.sessionId);

  const skipReviewPath = async (
    checkpoint: PendingCheckpoint,
    mode: "day" | "always",
  ) => {
    if (submissions.pending(checkpoint.checkpointId)) return;
    const path = checkpoint.content_apply?.path?.trim();
    const tool = checkpoint.content_apply?.tool?.trim() || "write";
    if (!path) return;
    const projectId = dependencies.projectId();
    if (!projectId) return;
    dependencies.onStart(checkpoint.checkpointId);
    await submissions.run(checkpoint.checkpointId, scope(checkpoint), async () => {
      const current = await dependencies.client.getReviewSettings(projectId);
      const rules = current.review_paths ?? [];
      const review_paths =
        mode === "always"
          ? rules.filter((r) => !reviewRuleMatches(r, tool, path))
          : rules.map((r) =>
              reviewRuleMatches(r, tool, path)
                ? {
                    ...r,
                    disabled_until: new Date(
                      Date.now() + 24 * 60 * 60 * 1000,
                    ).toISOString(),
                  }
                : r,
            );
      await dependencies.client.updateReviewSettings({ review_paths }, projectId);
      await resolveContentApply(dependencies.client, checkpoint, "approve");
    });
  };

  // Only an explicit No attaches composer guidance.
  const captureGuidance = (withGuidance?: boolean) => {
    if (!withGuidance) return { draft: "", secrets: [], secretAttachmentIds: [] };
    const secretAttachments = pendingAttachmentsForSession(dependencies.sessionId).filter(
      (attachment) => attachment.kind === "secret",
    );
    return {
      draft: composerDraftForSession(dependencies.sessionId),
      secrets: promptPartsFromPendingAttachments(secretAttachments).secrets,
      secretAttachmentIds: secretAttachments.map((attachment) => attachment.id),
    };
  };

  const onToolApproval = async (
    checkpoint: PendingCheckpoint,
    action: "approve" | "reject",
    options?: { optionId?: string; withGuidance?: boolean },
  ) => {
    if (submissions.pending(checkpoint.checkpointId)) return;
    const captured = action === "reject" ? captureGuidance(options?.withGuidance) : captureGuidance();
    const guidance = captured.draft.trim();
    dependencies.onStart(checkpoint.checkpointId);
    const completed = await submissions.run(checkpoint.checkpointId, scope(checkpoint), async () => {
      await resolveToolApproval(
        dependencies.client,
        checkpoint,
        action,
        {
          optionId: options?.optionId,
          guidance: guidance || undefined,
          secrets: captured.secrets,
        },
      );
    });
    if (completed) await consumeAttachedGuidance(captured);
  };

  const consumeAttachedGuidance = async (captured: ReturnType<typeof captureGuidance>) => {
    if (!captured.draft.trim() && captured.secretAttachmentIds.length === 0) return;
    if (composerDraftForSession(dependencies.sessionId) !== captured.draft) return;
    const destinationProjectId = dependencies.projectId();
    if (!destinationProjectId) return;
    try {
      await replaceComposerDocumentDraft(
        { projectId: destinationProjectId, sessionId: dependencies.sessionId },
        "",
      );
      for (const attachmentId of captured.secretAttachmentIds) {
        await removeComposerDocumentAttachment(
        { projectId: destinationProjectId, sessionId: dependencies.sessionId },
          attachmentId,
      );
      }
      await flushComposerDraftsToDisk();
    } catch (error) {
      dependencies.reportError(error);
    }
  };

  const onContentApply = async (
    checkpoint: PendingCheckpoint,
    body: {
      decision: "approve" | "reject" | "approve_partial";
      approvedHunks?: string[];
      withGuidance?: boolean;
    },
  ) => {
    if (submissions.pending(checkpoint.checkpointId)) return;
    const captured = body.decision === "reject" ? captureGuidance(body.withGuidance) : captureGuidance();
    const guidance = captured.draft.trim();
    dependencies.onStart(checkpoint.checkpointId);
    const completed = await submissions.run(checkpoint.checkpointId, scope(checkpoint), async () => {
      await resolveContentApply(
        dependencies.client,
        checkpoint,
        body.decision,
        {
          approvedHunks: body.approvedHunks,
          guidance: guidance || undefined,
          secrets: captured.secrets,
        },
      );
    });
    if (completed) await consumeAttachedGuidance(captured);
  };

  return { ...submissions, onToolApproval, onContentApply, skipReviewPath };
}
