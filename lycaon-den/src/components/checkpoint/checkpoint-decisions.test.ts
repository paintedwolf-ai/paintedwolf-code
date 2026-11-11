import { createRoot } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { checkpoint, contentCheckpoint } from "./approval-card-test-fixtures.ts";
import { createCheckpointDecisions } from "./checkpoint-decisions.ts";

const composer = vi.hoisted(() => ({
  draft: "",
  attachments: [] as { id: string; kind: "secret" }[],
  replace: vi.fn(async () => {}),
  remove: vi.fn(async () => {}),
  flush: vi.fn(async () => {}),
}));
vi.mock("../../chat/composer/composer-drafts.ts", () => ({
  composerDraftForSession: () => composer.draft,
  flushComposerDraftsToDisk: composer.flush,
}));
vi.mock("../../chat/composer/composer-attachment-store.ts", () => ({ pendingAttachmentsForSession: () => composer.attachments }));
vi.mock("../../chat/composer/prompt-parts.ts", () => ({ promptPartsFromPendingAttachments: () => ({ secrets: [] }) }));
vi.mock("../../chat/composer/composer-document-store.ts", () => ({
  replaceComposerDocumentDraft: composer.replace,
  removeComposerDocumentAttachment: composer.remove,
}));

beforeEach(() => {
  vi.clearAllMocks();
  composer.draft = "";
  composer.attachments = [];
});

function decisions(client = stubClient({ resolveCheckpoint: vi.fn().mockResolvedValue(undefined) }), projectId: string | null = "project") {
  return createRoot((dispose) => {
    const reportError = vi.fn();
    return { owner: createCheckpointDecisions({ client, projectId: () => projectId ?? undefined, sessionId: () => "session-1", onStart: vi.fn(), reportError }), reportError, dispose };
  });
}

const failed = () => new LycaonApiError("The action is still waiting.", 422, "approval_capacity_exceeded");

describe("checkpoint decisions", () => {
  it("preserves rejection guidance on failure and consumes it only after commitment", async () => {
    composer.draft = "Use another path";
    composer.attachments = [{ id: "attachment", kind: "secret" }];
    const resolveCheckpoint = vi.fn().mockRejectedValueOnce(failed()).mockResolvedValue(undefined);
    const { owner, dispose } = decisions(stubClient({ resolveCheckpoint }));
    await owner.onToolApproval(checkpoint(), "reject", { withGuidance: true });
    expect(composer.replace).not.toHaveBeenCalled();
    expect(composer.remove).not.toHaveBeenCalled();
    expect(owner.notice("checkpoint-1")?.code).toBe("approval_capacity_exceeded");
    await owner.onToolApproval(checkpoint(), "reject", { withGuidance: true });
    expect(resolveCheckpoint.mock.calls[1]?.[2]).toMatchObject({ action: "reject", guidance: "Use another path" });
    expect(composer.replace).toHaveBeenCalledWith({ projectId: "project", sessionId: "session-1" }, "");
    expect(composer.remove).toHaveBeenCalledWith({ projectId: "project", sessionId: "session-1" }, "attachment");
    expect(composer.flush).toHaveBeenCalledOnce();
    dispose();
  });

  it("does not consume a draft edited while the decision was pending", async () => {
    composer.draft = "Original direction";
    const resolveCheckpoint = vi.fn(async () => { composer.draft = "New direction"; });
    const { owner, dispose } = decisions(stubClient({ resolveCheckpoint }));
    await owner.onToolApproval(checkpoint(), "reject", { withGuidance: true });
    expect(composer.replace).not.toHaveBeenCalled();
    dispose();
  });

  it("keeps cleanup errors separate from committed decisions", async () => {
    composer.draft = "Direction";
    const error = new Error("composer cleanup failed");
    composer.replace.mockRejectedValueOnce(error);
    const { owner, reportError, dispose } = decisions();
    await owner.onToolApproval(checkpoint(), "reject", { withGuidance: true });
    expect(reportError).toHaveBeenCalledWith(error);
    expect(owner.notice("checkpoint-1")).toBeUndefined();
    expect(owner.pending("checkpoint-1")).toBe(true);
    dispose();
  });

  it("sends only the chosen approval and prevents another answer after acceptance", async () => {
    composer.draft = "Unrelated draft";
    const resolveCheckpoint = vi.fn().mockResolvedValue(undefined);
    const { owner, dispose } = decisions(stubClient({ resolveCheckpoint }));
    await owner.onToolApproval(checkpoint(), "approve", { optionId: "allow_for_chat" });
    await owner.onToolApproval(checkpoint(), "reject");
    expect(resolveCheckpoint).toHaveBeenCalledExactlyOnceWith("session-1", "checkpoint-1", { kind: "tool_approval", action: "approve", option_id: "allow_for_chat" });
    expect(composer.replace).not.toHaveBeenCalled();
    dispose();
  });

  it("preserves the host hunk selection and rejection guidance for content decisions", async () => {
    composer.draft = "Keep this section";
    const resolveCheckpoint = vi.fn().mockResolvedValue(undefined);
    const { owner, dispose } = decisions(stubClient({ resolveCheckpoint }));
    const card = contentCheckpoint();
    await owner.onContentApply(card, { decision: "reject", withGuidance: true });
    expect(resolveCheckpoint.mock.calls[0]?.[2]).toMatchObject({ kind: "content_apply", decision: "reject", guidance: "Keep this section" });
    await owner.onContentApply({ ...card, checkpointId: "partial" }, { decision: "approve_partial", approvedHunks: ["hunk"] });
    expect(resolveCheckpoint.mock.calls[1]?.[2]).toMatchObject({ decision: "approve_partial", approved_hunks: ["hunk"] });
    dispose();
  });

  it("binds guidance cleanup to the chat and project that submitted it", async () => {
    composer.draft = "Original direction";
    let sessionId = "session-1";
    let projectId = "project-1";
    const resolveCheckpoint = vi.fn(async () => { sessionId = "session-2"; projectId = "project-2"; });
    const { owner, dispose } = createRoot((dispose) => ({
      owner: createCheckpointDecisions({ client: stubClient({ resolveCheckpoint }), sessionId: () => sessionId, projectId: () => projectId, onStart: vi.fn(), reportError: vi.fn() }), dispose,
    }));
    await owner.onToolApproval(checkpoint(), "reject", { withGuidance: true });
    expect(composer.replace).toHaveBeenCalledWith({ projectId: "project-1", sessionId: "session-1" }, "");
    expect(composer.replace).not.toHaveBeenCalledWith({ projectId: "project-2", sessionId: "session-2" }, "");
    dispose();
  });

  it.each(["day", "always"] as const)("applies the selected %s review preference before answering", async (mode) => {
    const updateReviewSettings = vi.fn().mockResolvedValue(undefined);
    const resolveCheckpoint = vi.fn().mockResolvedValue(undefined);
    const client = stubClient({
      getReviewSettings: vi.fn(async () => ({ review_paths: [{ path: "**" }, { tool: "other", path: "**" }, { path: "/elsewhere/**" }] })),
      updateReviewSettings,
      resolveCheckpoint,
    });
    const { owner, dispose } = decisions(client);
    await owner.skipReviewPath(contentCheckpoint(), mode);
    expect(resolveCheckpoint).toHaveBeenCalledOnce();
    const rules = updateReviewSettings.mock.calls[0]?.[0].review_paths;
    if (mode === "day") expect(rules?.[0]?.disabled_until).toEqual(expect.any(String));
    else expect(rules).toHaveLength(2);
    dispose();
  });

  it("leaves unavailable review-preference operations alone", async () => {
    const resolveCheckpoint = vi.fn().mockResolvedValue(undefined);
    const { owner, dispose } = decisions(stubClient({ resolveCheckpoint }), null);
    await owner.skipReviewPath(contentCheckpoint(), "always");
    await owner.skipReviewPath(checkpoint(), "day");
    expect(resolveCheckpoint).not.toHaveBeenCalled();
    dispose();
  });
});
