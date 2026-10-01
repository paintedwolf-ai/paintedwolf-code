import { Show, createEffect, createMemo, createSignal } from "solid-js";
import type {
  CheckpointDecisionMeta,
  CheckpointStatus,
} from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import {
  approvalGrantsError,
  hasGrant,
  hasQuiet,
  isApprovalGrantsCacheLoaded,
  loadApprovalGrantsCache,
  revokeGrantsOptimistic,
} from "../../settings/security/approval-grants-cache.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { useTranscriptDisclosure } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { ApprovalContentLink } from "./ApprovalContentLink.tsx";
import { reviewPendingApproval } from "../../chat/checkpoint/approval-review.ts";
import {
  DecisionRow,
  decisionRowLabel,
  type DecisionTone,
} from "../transcript/DecisionRow.tsx";

type Props = {
  meta: CheckpointDecisionMeta;
  sessionId?: string | null;
  projectId?: string;
  entryKey?: string;
  client?: LycaonClient | null;
  onError?: (err: unknown) => void;
};

function decisionLabel(meta: CheckpointDecisionMeta): string {
  const table = APPROVALS_COPY.card.decision;
  const byKind = table[meta.kind as keyof typeof table];
  if (byKind && typeof byKind === "object" && meta.status in byKind) {
    return (byKind as Record<string, string>)[meta.status] ?? meta.status;
  }
  return table.fallback[meta.status] ?? meta.status;
}

function grantLine(meta: CheckpointDecisionMeta): string | undefined {
  const title = meta.grant_title?.trim();
  const scope = meta.grant_scope?.trim();
  if (!title) return undefined;
  return scope ? `${title} · ${scope}` : title;
}

function decisionAriaLabel(meta: CheckpointDecisionMeta): string {
  const tool = meta.tool?.trim();
  const subject = meta.subject?.trim();
  const causing = meta.causing_command?.trim();
  const location = meta.location?.trim();
  const guidance = meta.guidance?.trim();
  return decisionRowLabel(decisionLabel(meta), [
    tool,
    subject && subject !== tool ? subject : undefined,
    location,
    causing
      ? `${APPROVALS_COPY.card.causedByLabel} ${causing}`
      : undefined,
    guidance
      ? `${APPROVALS_COPY.card.rejectGuidance.directionLabel}: ${guidance}`
      : undefined,
    grantLine(meta),
  ]);
}

function decisionTone(status: CheckpointStatus): DecisionTone {
  if (status === "approved") return "approved";
  if (status === "rejected") return "rejected";
  if (status === "pending") return "pending";
  return "neutral";
}

function DecisionChrome(props: {
  meta: CheckpointDecisionMeta;
  tool?: string;
  subject?: string;
  location?: string;
  causing?: string;
  grant?: string;
  truncateSubject?: boolean;
}) {
  return (
    <DecisionRow
      tone={decisionTone(props.meta.status)}
      label={decisionLabel(props.meta)}
      details={[
        { text: props.tool ?? "", testId: "checkpoint-decision-tool" },
        {
          text: props.subject ?? "",
          emphasis: "subject",
          truncate: props.truncateSubject,
          testId: "checkpoint-decision-subject",
        },
        {
          text: props.location ?? "",
          truncate: props.truncateSubject,
          testId: "checkpoint-decision-location",
        },
        {
          text: props.causing
            ? `${APPROVALS_COPY.card.causedByLabel} ${props.causing}`
            : "",
          truncate: props.truncateSubject,
          testId: "checkpoint-decision-causing",
        },
        {
          text: props.grant ?? "",
          emphasis: "grant",
          truncate: props.truncateSubject,
          testId: "checkpoint-decision-grant",
        },
      ]}
    />
  );
}

function RevokeButton(props: {
  grantIds: string[];
  client: LycaonClient;
  onError?: (err: unknown) => void;
}) {
  const [revoking, setRevoking] = createSignal(false);
  const revoke = async (event: MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    if (revoking()) return;
    setRevoking(true);
    try {
      const res = await props.client.revokeApprovalGrants({
        ids: props.grantIds,
      });
      const revoked = res.results
        .filter((result) => result.revoked)
        .map((result) => result.id);
      revokeGrantsOptimistic(revoked);
      const failed = res.results.find((result) => !result.revoked);
      if (failed) {
        throw new Error(failed.message || failed.code || "revoke failed");
      }
    } catch (err) {
      props.onError?.(err);
    } finally {
      setRevoking(false);
    }
  };
  return (
    <button
      type="button"
      class="den-checkpoint-decision-chicklet__revoke"
      data-testid="checkpoint-decision-revoke"
      disabled={revoking()}
      onClick={(e) => void revoke(e)}
    >
      {APPROVALS_COPY.revokeButton}
    </button>
  );
}

export function CheckpointDecisionChicklet(props: Props) {
  const entryOpts = () =>
    props.entryKey
      ? { sessionId: props.sessionId ?? undefined, entryKey: props.entryKey }
      : undefined;
  const { bindTranscriptEntry } = useTranscriptEntry(entryOpts);
  const { key: disclosureKey, open, onToggle, onSummaryClick } = useTranscriptDisclosure(() =>
    props.entryKey ? transcriptDisclosureKey.checkpoint(props.entryKey) : undefined,
  );

  const status = (): CheckpointStatus => props.meta.status;
  const tool = () => props.meta.tool?.trim() || undefined;
  const subject = () => {
    const s = props.meta.subject?.trim();
    if (!s) return undefined;
    const t = tool();
    if (t && s === t) return undefined;
    return s;
  };
  const causing = () => props.meta.causing_command?.trim() || undefined;
  const location = () => props.meta.location?.trim() || undefined;
  const guidance = () => props.meta.guidance?.trim() || undefined;
  const grant = () => grantLine(props.meta);
  const grantIds = () =>
    (props.meta.grant_ids ?? []).map((id) => id.trim()).filter(Boolean);

  createEffect(() => {
    const client = props.client;
    const ids = grantIds();
    if (!client || ids.length === 0) return;
    // Settings invalidation retries only an unloaded, error-free cache.
    if (isApprovalGrantsCacheLoaded() || approvalGrantsError()) return;
    void loadApprovalGrantsCache(client).catch(() => undefined);
  });

  const revokeCtx = createMemo(() => {
    const ids = grantIds();
    const client = props.client;
    if (ids.length === 0 || !client) return undefined;
    if (!isApprovalGrantsCacheLoaded()) return undefined;
    if (!ids.some((id) => hasGrant(id) || hasQuiet(id))) return undefined;
    return { grantIds: ids, client };
  });
  const expandable = () =>
    status() === "pending" ||
    Boolean(subject() || location() || causing() || guidance() || grant());
  const statusClass = () => ({
    "den-checkpoint-decision-chicklet--approved": status() === "approved",
    "den-checkpoint-decision-chicklet--rejected": status() === "rejected",
    "den-checkpoint-decision-chicklet--expired": status() === "expired",
    "den-checkpoint-decision-chicklet--pending": status() === "pending",
  });

  return (
    <Show
      when={expandable()}
      fallback={
        <div
          ref={bindTranscriptEntry}
          class="den-checkpoint-decision-chicklet"
          classList={statusClass()}
          data-testid="checkpoint-decision-chicklet"
          data-status={status()}
          data-kind={props.meta.kind}
          data-checkpoint-id={props.meta.checkpoint_id}
          role="status"
          aria-label={decisionAriaLabel(props.meta)}
        >
          <DecisionChrome meta={props.meta} tool={tool()} />
          <Show when={status() === "pending"}>
            <span
              data-testid="checkpoint-open-marker"
              data-checkpoint-id={props.meta.checkpoint_id}
              class="sr-only"
            />
          </Show>
        </div>
      }
    >
      <details
        ref={bindTranscriptEntry}
        class="den-checkpoint-decision-chicklet den-checkpoint-decision-chicklet--expandable den-transcript-disclosure-card"
        classList={statusClass()}
        data-testid="checkpoint-decision-chicklet"
        data-status={status()}
        data-kind={props.meta.kind}
        data-checkpoint-id={props.meta.checkpoint_id}
        data-disclosure-key={disclosureKey}
        open={open()}
        onToggle={onToggle}
      >
        <Show when={status() === "pending"}>
          <span
            data-testid="checkpoint-open-marker"
            data-checkpoint-id={props.meta.checkpoint_id}
            class="sr-only"
          />
        </Show>
        <summary
          class="den-checkpoint-decision-chicklet__summary"
          onClick={onSummaryClick}
          aria-label={decisionAriaLabel(props.meta)}
        >
          <span class="den-checkpoint-decision-chicklet__summary-main">
            <DecisionChrome
              meta={props.meta}
              tool={tool()}
              subject={subject()}
              location={location()}
              causing={causing()}
              grant={grant()}
              truncateSubject
            />
          </span>
          <Show when={revokeCtx()} keyed>
            {(ctx) => (
              <RevokeButton
                grantIds={ctx.grantIds}
                client={ctx.client}
                onError={props.onError}
              />
            )}
          </Show>
          <span class="den-tool-chicklet-caret" aria-hidden="true" />
        </summary>
        <div class="den-checkpoint-decision-chicklet__body" data-testid="checkpoint-decision-body">
          <div class="den-tool-part-card-section">
            <dl class="den-tool-part-card-facts">
              <div>
                <dt>{status() === "pending" ? "Status" : "Decision"}</dt>
                <dd data-testid="checkpoint-decision-body-status">{decisionLabel(props.meta)}</dd>
              </div>
              <Show when={tool()}>
                {(t) => (
                  <div>
                    <dt>Tool</dt>
                    <dd data-testid="checkpoint-decision-body-tool">
                      <code>{t()}</code>
                    </dd>
                  </div>
                )}
              </Show>
              <Show when={subject()}>
                {(s) => (
                  <div class="den-tool-part-card-facts--wide">
                    <dt>Action</dt>
                    <dd data-testid="checkpoint-decision-body-action">
                      <code>{s()}</code>
                    </dd>
                  </div>
                )}
              </Show>
              <Show when={location()}>
                {(loc) => (
                  <div class="den-tool-part-card-facts--wide">
                    <dt>Location</dt>
                    <dd data-testid="checkpoint-decision-body-location">
                      <code>{loc()}</code>
                    </dd>
                  </div>
                )}
              </Show>
              <Show when={causing()}>
                {(c) => (
                  <div class="den-tool-part-card-facts--wide">
                    <dt>{APPROVALS_COPY.card.causedByLabel}</dt>
                    <dd data-testid="checkpoint-decision-body-causing">
                      <code>{c()}</code>
                    </dd>
                  </div>
                )}
              </Show>
              <Show when={grant()}>
                {(g) => (
                  <div class="den-tool-part-card-facts--wide">
                    <dt>Permission</dt>
                    <dd data-testid="checkpoint-decision-body-grant">{g()}</dd>
                  </div>
                )}
              </Show>
              <Show when={guidance()}>
                {(dir) => (
                  <div class="den-tool-part-card-facts--wide">
                    <dt>{APPROVALS_COPY.card.rejectGuidance.directionLabel}</dt>
                    <dd data-testid="checkpoint-decision-body-guidance">{dir()}</dd>
                  </div>
                )}
              </Show>
            </dl>
          </div>
          <div class="den-checkpoint-decision-chicklet__actions">
            <Show when={status() === "pending" && props.sessionId}>
              <button
                type="button"
                class="den-checkpoint-decision-chicklet__review-btn"
                data-testid="checkpoint-decision-review-dock"
                onClick={() => {
                  if (props.sessionId) {
                    reviewPendingApproval({
                      sessionId: props.sessionId,
                      checkpointId: props.meta.checkpoint_id,
                    });
                  }
                }}
              >
                Review in composer
              </button>
            </Show>
            <ApprovalContentLink
              label="Approval details"
              pane="decision"
              identity={{
                checkpointId: props.meta.checkpoint_id,
                sessionId: props.sessionId,
                projectId: props.projectId,
              }}
              text={() =>
                [
                  `Decision: ${decisionLabel(props.meta)}`,
                  tool() && `Tool: ${tool()}`,
                  subject() && `Action: ${subject()}`,
                  location() && `Location: ${location()}`,
                  causing() && `${APPROVALS_COPY.card.causedByLabel}: ${causing()}`,
                  grant() && `Permission: ${grant()}`,
                  guidance() &&
                    `${APPROVALS_COPY.card.rejectGuidance.directionLabel}: ${guidance()}`,
                ]
                  .filter(Boolean)
                  .join("\n\n")
              }
            />
          </div>
        </div>
      </details>
    </Show>
  );
}
