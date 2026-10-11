import { IgnoreSecretDialog } from "../source/secrets/IgnoreSecretDialog.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { For, Show, createMemo, createSignal, createEffect, on, onCleanup, onMount } from "solid-js";
import type { ApprovalTarget } from "../../api/types.ts";
import { approvalArgsPreview, approvalCausingCommand } from "../../chat/checkpoint/approval-display.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { revealApprovalOrigin } from "../../chat/checkpoint/approval-reveal.ts";
import { approvalSlot } from "../../chat/checkpoint/approval-slots.ts";
import { ActionsZone, approvalKeyHandler, ContextZone, GrantMenu, RedirectRail, ShellCard, ShellHeader, ShellScroll, SubjectBlock, SubjectFact, SubjectLocation, SubjectTargets, useApprovalCollapse } from "./ApprovalShell.tsx";
import { ApprovalContentLink } from "./ApprovalContentLink.tsx";
import { ApprovalFileChanges } from "./ApprovalFileChanges.tsx";
import { checkpointRowLabel } from "./checkpoint-row-label.ts";
import { approvalWaitLabel } from "../../chat/checkpoint/approval-wait.ts";
import { presenceAvailable } from "../../platform/presence.ts";
import { chatUnlocked } from "../../chat/vault/chat-vault-store.ts";
import type { ApprovalCardProps } from "./approval-card-types.ts";
import { effectiveConsequence, useHighRisk } from "./approval-consequence.ts";
import { useToolApprovalChoices } from "./tool-approval-choices.ts";
import { approvalEvidenceRows } from "./approval-evidence.ts";

export function ToolApprovalBody(props: ApprovalCardProps) {
  const [ignoreOpen, setIgnoreOpen] = createSignal(false);
  const [ignoreSaved, setIgnoreSaved] = createSignal(false);
  createEffect(on(() => props.checkpoint.checkpointId, () => { setIgnoreOpen(false); setIgnoreSaved(false); }));
  const copy = APPROVALS_COPY.card;
  const payload = () => props.checkpoint.tool_approval;
  const plan = () => payload()?.plan;
  const presentation = () => plan()?.presentation;
  const elevatedTip = createMemo(() => {
    const effects = plan()?.elevated_effects;
    return effects?.length ? copy.elevated.tooltip(effects) : undefined;
  });
  const ignoreReview = createMemo(() => {
    const client = props.client;
    const projectId = props.projectId;
    const candidateId = presentation()?.ignore_candidate_id;
    return client && projectId && candidateId ? { client, projectId, candidateId } : null;
  });
  const { highRisk, consequenceText, impactText, labelId, impactId, describedBy } =
    useHighRisk(
      () => effectiveConsequence(payload()),
      () => presentation()?.impact ?? "",
    );
  const collapse = useApprovalCollapse(props);
  const [optionsOpen, setOptionsOpen] = createSignal(false);

  const targets = createMemo(() => plan()?.subject.targets ?? []);
  const secretShape = createMemo(() => {
    if (plan()?.subject.kind !== "secret") return "";
    const shape = targets()[0]?.details?.generic_shape;
    return typeof shape === "string" ? shape.trim() : "";
  });
  const causingCommand = createMemo(() => approvalCausingCommand(payload()));
  const location = createMemo(() => {
    const loc = presentation()?.location;
    const origin = loc?.origin?.trim() ?? "";
    const destination = loc?.destination?.trim() ?? "";
    if (!origin || !destination) return undefined;
    const kind = loc?.origin_kind;
    if (kind !== "file" && kind !== "field") return undefined;
    return {
      origin,
      destination,
      originKind: kind,
      destination_kind: loc?.destination_kind,
      recipients: loc?.recipients ?? [],
      secretNames: loc?.secret_names ?? [],
      path: loc?.path?.trim() ?? "",
      line: loc?.line,
      revealToolCallId: loc?.reveal_tool_call_id?.trim() ?? "",
    };
  });
  const displayedTargets = createMemo(() => {
    if (plan()?.subject.kind !== "action_set" || !location()?.secretNames.length) return targets();
    return targets().filter((target) => target.kind !== "secret");
  });
  const showInChat = (toolCallId: string) => {
    const id = toolCallId.trim();
    if (!id) return;
    if (props.onShowInChat) props.onShowInChat(id);
    else revealApprovalOrigin({ toolCallId: id, sessionId: props.sessionId ?? "" });
  };
  const onLocationOrigin = () => {
    const loc = location();
    if (!loc) return;
    if (loc.originKind === "file" && loc.path && props.projectId) {
      void openSourceLocation({
        projectId: props.projectId,
        path: loc.path,
        line: loc.line,
        intent: "permanent",
      });
      return;
    }
    if (loc.revealToolCallId) showInChat(loc.revealToolCallId);
  };
  const { directoryScopes, directoryScope, setChosenDirectory, recommended, held, heldBlocked, heldReaders, recommendedDisabled, alternatives, grantMenuNote, pickSlot, faceMeta, selectOption } = useToolApprovalChoices(props);
  const [nowMs, setNowMs] = createSignal(Date.now());
  onMount(() => {
    const tick = setInterval(() => setNowMs(Date.now()), 30_000);
    onCleanup(() => clearInterval(tick));
  });
  const waitLabel = createMemo(() => approvalWaitLabel(props.checkpoint.issuedAt, nowMs()));
  const packageIdentityFacts = (target: ApprovalTarget) => {
    const detail = target.details ?? {};
    const status = typeof detail.identity_status === "string"
      ? detail.identity_status
      : "unavailable";
    let identity: string = copy.packageIdentity.unavailable;
    if (status === "resolved") identity = copy.packageIdentity.resolved;
    else if (status === "not_found") identity = copy.packageIdentity.notFound;
    else if (status === "unsupported") identity = copy.packageIdentity.unsupported;
    const facts: string[] = [identity];
    if (typeof detail.age_days === "number" && detail.age_days >= 0) {
      facts.push(detail.age_days === 0
        ? copy.packageIdentity.publishedToday
        : copy.packageIdentity.publishedDaysAgo(detail.age_days));
    }
    if (detail.verified_attestation === true) {
      facts.push(copy.packageIdentity.verifiedSource);
    }
    return facts.join(" · ");
  };
  const allowedHosts = (target: ApprovalTarget) =>
    (Array.isArray(target.details?.allowed_hosts)
      ? target.details.allowed_hosts
          .filter((value): value is string => typeof value === "string")
          .map((value) => value.trim())
          .filter(Boolean)
      : []
    ).sort();
  const approvedReadPaths = (target: ApprovalTarget) =>
    (Array.isArray(target.details?.approved_read_paths)
      ? target.details.approved_read_paths.filter(
          (value): value is string => typeof value === "string" && value.trim().length > 0,
        )
      : []
    ).sort();
  // Equal host facts share one boundary description.
  const sharedRegistryHosts = createMemo(() => {
    if (plan()?.subject.kind !== "package_set") return undefined;
    const members = targets();
    const [first] = members;
    if (!first || members.length < 2) return undefined;
    const keys = new Set(members.map((t) => JSON.stringify([allowedHosts(t), approvedReadPaths(t)])));
    return keys.size === 1 ? allowedHosts(first) : undefined;
  });
  const sharedPackageBoundary = createMemo(() => {
    const hosts = sharedRegistryHosts();
    const first = targets()[0];
    return hosts && first ? copy.packageIdentity.executionBoundary(hosts, approvedReadPaths(first)) : undefined;
  });
  const targetPreview = (target: ApprovalTarget) => {
    if (plan()?.subject.kind === "package_set") {
      const facts = packageIdentityFacts(target);
      return sharedRegistryHosts()
        ? facts
        : `${facts}\n${copy.packageIdentity.executionBoundary(allowedHosts(target), approvedReadPaths(target))}`;
    }
    const args = target.details?.args;
    return args && typeof args === "object" && !Array.isArray(args)
      ? approvalArgsPreview(args as Record<string, unknown>)
      : "";
  };
  const directIPDeclaration = createMemo(() => {
    if (plan()?.subject.kind !== "direct_ip") return undefined;
    const raw = targets()[0]?.details?.declared_destinations;
    return Array.isArray(raw)
      ? raw
          .filter((value): value is string => typeof value === "string")
          .map((value) => value.trim())
          .filter(Boolean)
      : [];
  });
  const socketResolutions = createMemo(() => {
    if (plan()?.subject.kind !== "socket_set") return [];
    return targets().flatMap((target) => {
      const resolved = target.details?.resolved_path;
      if (typeof resolved !== "string" || !resolved.trim()) return [];
      return [resolved.trim()];
    });
  });
  const agent = createMemo(() => {
    const p = payload();
    if (!p || (!p.ai_rationale?.trim() && !p.ai_rationale_pending)) return undefined;
    return {
      text: p.ai_rationale,
      pending: p.ai_rationale_pending,
      onceKey: props.checkpoint.checkpointId,
    };
  });
  const detailRows = createMemo(() => approvalEvidenceRows(plan(), directIPDeclaration(), socketResolutions()));

  const joinedWaiterCount = createMemo(() => {
    const count = payload()?.joined_count;
    return typeof count === "number" && count > 1 ? count : 0;
  });

  const repeatBlock = createMemo(() => {
    const repeat = payload()?.repeat;
    if (!repeat || (repeat.count <= 1 && (repeat.asks ?? 0) <= 1)) return undefined;
    return repeat;
  });

  const onCardKeyDown = approvalKeyHandler({
    enabled: () =>
      !props.resolving && !collapse.minimized() && Boolean(recommended()),
    onPrimary: () => {
      const option = recommended();
      if (option && !option.disabled) selectOption(option.id);
    },
    onNo: () => props.onToolApproval("reject", { withGuidance: false }),
    onGrantMenu: alternatives().length > 0 ? () => setOptionsOpen(true) : undefined,
    onSlot: pickSlot,
  });

  return (
    <ShellCard
      testid="tool-approval-card"
      cardId={props.checkpoint.checkpointId}
      onKeyDown={onCardKeyDown}
      projectId={props.projectId}
      sessionId={props.sessionId ?? props.checkpoint.sessionId}
      highRisk={highRisk()}
      elevatedTip={elevatedTip()}
      describedBy={describedBy()}
      inert={props.inert}
      collapse={collapse}
      peek={checkpointRowLabel(props.checkpoint)}
      header={
        <ShellHeader
          state={copy.state.needsApproval}
          action={presentation()?.action ?? "Review action"}
          highRisk={highRisk()}
          highRiskLabelId={labelId}
          elevatedTip={elevatedTip()}
          chip={presentation()?.tool}
          chipTestid="approval-tool"
          wait={waitLabel()}
        />
      }
    >
      <ShellScroll>
      <div class="den-approval-card-body">
        <div data-zone="subject">
          <Show
            when={
              // Singleton sets still need previews.
              displayedTargets().length > 1 || plan()?.subject.kind === "action_set" ||
              plan()?.subject.kind === "package_set"
            }
            fallback={
              <Show keyed when={displayedTargets()[0]}>
                {(target) => (
                  <SubjectBlock text={target.label} testid="approval-subject" />
                )}
              </Show>
            }
          >
            <SubjectTargets
              title={plan()?.subject.title ?? ""}
              kind={plan()?.subject.kind}
              targets={displayedTargets()}
              preview={targetPreview}
              sharedFact={sharedPackageBoundary()}
            />
          </Show>
          <Show when={directoryScopes().length > 1}>
            <div class="den-approval-directory-scope" onKeyDown={(event) => event.stopPropagation()}>
              <span>{copy.directoryScope}</span>
              <DenSelect
                aria-label={copy.directoryScope}
                options={directoryScopes().map((path) => ({ value: path, label: path }))}
                value={directoryScope()}
                disabled={props.resolving}
                onValueChange={setChosenDirectory}
              />
            </div>
          </Show>
          <Show when={location()} keyed>
            {(loc) => (
              <>
                <Show when={loc.secretNames.length > 0}>
                  <SubjectFact
                    label={copy.protectedValuesLabel}
                    value={loc.secretNames.join(", ")}
                    testid="approval-secret-names"
                  />
                </Show>
                <Show
                  when={loc.recipients.length > 1}
                  fallback={
                    <SubjectLocation
                      destinationKind={loc.destination_kind}
                      origin={loc.origin}
                      destination={loc.destination}
                      originKind={loc.originKind}
                      onOrigin={onLocationOrigin}
                    />
                  }
                >
                  <For each={loc.recipients}>
                    {(recipient) => (
                      <SubjectLocation
                        destinationKind={recipient.kind}
                        origin={loc.origin}
                        destination={recipient.label}
                        originKind={loc.originKind}
                        onOrigin={onLocationOrigin}
                      />
                    )}
                  </For>
                </Show>
              </>
            )}
          </Show>
          <Show when={secretShape()} keyed>
            {(shape) => (
              <SubjectFact
                label={copy.secretShapeLabel}
                value={shape}
                testid="approval-secret-shape"
              />
            )}
          </Show>
          <Show when={causingCommand()} keyed>
            {(command) => (
              <SubjectFact
                label={copy.causedByLabel}
                value={command}
                testid="approval-causing-command"
              />
            )}
          </Show>
        </div>

        <ApprovalFileChanges
          changes={presentation()?.file_changes}
          projectId={props.projectId}
          checkpointId={props.checkpoint.checkpointId}
        />

        <ContextZone
          lead={presentation()?.lead}
          impact={impactText()}
          impactTone={highRisk() ? "risk" : "standard"}
          impactId={impactId}
          impactTestid={
            consequenceText() ? "approval-high-risk-consequence" : "approval-impact"
          }
          note={presentation()?.option_note}
          noteTestid="approval-option-note"
          agent={agent()}
          detailRows={detailRows()}
          detailActions={<Show when={location()?.originKind === "file" && location()?.revealToolCallId}>
            <button type="button" class="den-approval-card-location-origin" data-testid="approval-show-in-chat"
              onClick={() => { const id = location()?.revealToolCallId; if (id) showInChat(id); }}>{copy.showInChat}</button>
          </Show>}
        />

        <Show when={joinedWaiterCount()}>
          {(count) => (
            <p class="den-settings-hint" data-testid="approval-joined-waiters">
              {copy.identicalCallsWaiting(count())}
            </p>
          )}
        </Show>

        <ShowLatest when={repeatBlock()}>{repeat => <div class="den-approval-repeat" data-testid="approval-repeat">
          <p class="den-settings-hint">{copy.repeat.summary(Math.max(repeat().count, repeat().asks ?? 0))}</p>
          <ApprovalContentLink label="Repeated requests" pane="repeat" text={() => [
            copy.repeat.scopeNote, ...(repeat().subjects ?? []),
            repeat().subjects_truncated ? copy.repeat.truncated(1) : "",
            (repeat().suppressed_count ?? 0) > 0 ? copy.repeat.suppressed(repeat().suppressed_count ?? 0) : "",
          ].filter(Boolean).join("\n\n")} />
        </div>}</ShowLatest>
        <Show when={held()} keyed>{(release) => (
          <section class="den-approval-held" data-testid="approval-held-release" aria-label={copy.held.title}>
            <p class="den-approval-held-title">{copy.held.title}: {release.secrets.map((secret) => secret.name).join(", ")}</p>
            <For each={heldReaders()}>{(line) => <p class="den-settings-hint">{line}</p>}</For>
          </section>
        )}</Show>
      </div>
      </ShellScroll>

      <Show when={ignoreSaved()}><p class="den-settings-hint" role="status">Project ignore saved. The held request still needs your decision.</p></Show>
      <Show when={ignoreReview()} keyed>{(review) => (
        <Show when={!ignoreSaved()}>
          <DenButton variant="link" disabled={props.resolving} onClick={() => setIgnoreOpen(true)}>Ignore this value in this project…</DenButton>
          <Show when={ignoreOpen()}>
            <IgnoreSecretDialog
              client={review.client}
              target={{ projectId: review.projectId, value: async () => (await review.client.getSecretIgnoreCandidate(review.projectId, review.candidateId)).value }}
              onClose={() => setIgnoreOpen(false)} onSaved={() => setIgnoreSaved(true)}
            />
          </Show>
        </Show>
      )}</Show>
      {/* What approving does stays beside the actions; it is one short line. */}
      <Show when={held()} keyed>{(release) => {
        const line = () => !presenceAvailable() ? copy.held.needsDesktop
          : chatUnlocked(release.chat_session_id) ? copy.held.unlocked : copy.held.confirm;
        return <p class="den-settings-hint den-approval-held-confirm" data-testid="approval-held-confirm" data-tip={line()}>{line()}</p>;
      }}</Show>
      <ActionsZone
        sessionId={props.sessionId}
        resolving={props.resolving}
        no={{
          label: copy.no,
          onNo: (withGuidance) =>
            props.onToolApproval("reject", { withGuidance }),
          testid: "approval-no",
        }}
        middle={
          alternatives().length > 0 ? (
            <GrantMenu
              face={{ title: copy.otherOptions }}
              items={alternatives().map((option) => ({
                id: option.id,
                title: option.title,
                meta: option.coverage,
                group: option.group,
                slot: approvalSlot(option),
                disabled: option.disabled || heldBlocked(option),
                note: heldBlocked(option) ? copy.held.needsDesktop : option.note,
              }))}
              note={grantMenuNote()}
              disabled={props.resolving}
              open={optionsOpen()}
              onOpenChange={setOptionsOpen}
              onPick={selectOption}
            />
          ) : undefined
        }
        primary={{
          label: recommended()?.title ?? copy.allow,
          onPrimary: () => {
            const option = recommended();
            if (option && !option.disabled) selectOption(option.id);
          },
          disabled: !recommended() || recommendedDisabled(),
          title: recommendedDisabled()
            ? (heldBlocked(recommended()) ? copy.held.needsDesktop : recommended()?.note || presentation()?.option_note)
            : undefined,
          meta: faceMeta(),
          testid: "approval-approve-primary",
        }}
      />
      <RedirectRail sessionId={props.sessionId} />
    </ShellCard>
  );
}
