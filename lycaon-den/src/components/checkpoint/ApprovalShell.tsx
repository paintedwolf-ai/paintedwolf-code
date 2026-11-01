import { InlineNotice } from "../../notices/InlineNotice.tsx";
import type { AppNotice } from "../../notices/notice-model.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { ApprovalContentLink, ApprovalContentProvider } from "./ApprovalContentLink.tsx";
import {
  For,
  Show,
  type JSX,
} from "solid-js";
import type {
  ApprovalSecretDestinationKind,
  ApprovalTarget,
} from "../../api/types.ts";
import { setLastFocusedCheckpointId } from "../../chat/checkpoint/redirect-target.ts";
import { composerDraftForSession } from "../../chat/composer/composer-drafts.ts";
import { pendingAttachmentsForSession } from "../../chat/composer/composer-attachment-store.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { RandomLetterReveal } from "../primitives/RandomLetterReveal.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import {
  createDockCollapse,
  DockCollapseBody,
  DockCollapseHead,
  type DockCollapse,
} from "../primitives/DockCollapse.tsx";

function composerGuidanceDraft(sessionId?: string): string {
  return composerDraftForSession(sessionId).trim();
}

function hasComposerGuidance(sessionId?: string): boolean {
  return composerGuidanceDraft(sessionId) !== "" ||
    pendingAttachmentsForSession(sessionId).some((attachment) => attachment.kind === "secret");
}

/** The body holds this so Enter on the restore strip cannot approve. */
export function useApprovalCollapse(p: {
  minimized?: boolean;
  onMinimizedChange?: (next: boolean) => void;
}): DockCollapse {
  return createDockCollapse({
    minimized: () => p.minimized,
    onMinimizedChange: (next) => p.onMinimizedChange?.(next),
  });
}

export function ShellCard(p: {
  testid: string;
  /** Stable id used to scope keyboard shortcuts. */
  cardId: string;
  sessionId?: string;
  projectId?: string;
  highRisk?: boolean;
  elevatedTip?: string;
  describedBy?: string;
  decisionNotice?: AppNotice;
  /** The card is leaving with the dock and takes no input. */
  inert?: boolean;
  collapse: DockCollapse;
  /** Card shortcuts; they see only keys pressed inside the card. */
  onKeyDown?: (event: KeyboardEvent) => void;
  /** Rendered only while expanded. */
  header: JSX.Element;
  /** One line naming the pending action, shown while minimized. */
  peek: string;
  children: JSX.Element;
}) {
  return (
    <ApprovalContentProvider value={() => ({checkpointId: p.cardId, sessionId: p.sessionId, projectId: p.projectId})}>
    <article
      class="den-checkpoint-card den-approval-card den-dock-collapse"
      data-testid={p.testid}
      data-checkpoint-id={p.cardId}
      data-consequence-band={p.highRisk ? "high_risk" : "standard"}
      data-minimized={p.collapse.minimized() ? "" : undefined}
      aria-describedby={p.describedBy || undefined}
      inert={p.inert || undefined}
      tabindex={0}
      onFocusIn={() => setLastFocusedCheckpointId(p.cardId)}
      onKeyDown={(event) => p.onKeyDown?.(event)}
    >
      <DockCollapseHead
        collapse={p.collapse}
        tag="header"
        class="den-approval-card-header"
        lead={() => <span class="den-approval-card-dot" aria-hidden="true" />}
        collapsedLead={() => (
          <>
            <Show when={p.elevatedTip} keyed>{(tip) => <ElevatedApprovalChip tip={tip} />}</Show>
            <Show when={p.highRisk}>
              <span
                class="den-approval-card-chip den-approval-card-chip--risk"
                data-testid="approval-peek-risk"
              >
                <HighRiskIcon />
                <span>{APPROVALS_COPY.card.highRisk.label}</span>
              </span>
            </Show>
          </>
        )}
        expanded={() => p.header}
        peek={p.peek}
        expandLabel={APPROVALS_COPY.card.collapse.expandLabel}
        minimizeLabel={APPROVALS_COPY.card.collapse.minimizeLabel}
        expandTestid="approval-card-expand"
        minimizeTestid="approval-card-minimize"
      />
      <DockCollapseBody>
        <InlineNotice notice={p.decisionNotice} testId="approval-decision-error" />
        {p.children}
      </DockCollapseBody>
    </article>
    </ApprovalContentProvider>
  );
}

export function ShellScroll(p: { children: JSX.Element }) {
  return (
    <Scrollport class="den-approval-card-scroll">
      {p.children}
    </Scrollport>
  );
}

export function ShellHeader(p: {
  state: string;
  action: string;
  highRisk?: boolean;
  highRiskLabelId?: string;
  elevatedTip?: string;
  reasonChip?: string;
  reasonChipTestid?: string;
  chip?: string;
  chipTestid?: string;
  path?: string;
  /** How long the card has waited; empty under a minute. */
  wait?: string;
}) {
  return (
    <div class="den-approval-card-title">
      <h3 class="den-approval-card-heading">
        <span>{p.state}</span>
        <span class="den-approval-card-heading-sep" aria-hidden="true">
          ·
        </span>
        <span class="den-approval-card-heading-action">{p.action}</span>
      </h3>
      <span class="den-approval-card-chips">
        <Show when={p.elevatedTip} keyed>
          {(tip) => <ElevatedApprovalChip tip={tip} />}
        </Show>
        <Show when={p.highRisk}>
          <span
            id={p.highRiskLabelId}
            class="den-approval-card-chip den-approval-card-chip--risk"
            data-testid="approval-high-risk-label"
          >
            <HighRiskIcon />
            <span>{APPROVALS_COPY.card.highRisk.label}</span>
          </span>
        </Show>
        <Show when={p.wait} keyed>
          {(age) => (
            <span
              class="den-approval-card-chip den-approval-card-chip--wait"
              data-testid="approval-wait"
            >
              {APPROVALS_COPY.card.waiting(age)}
            </span>
          )}
        </Show>
        <Show when={p.reasonChip}>
          {(name) => (
            <span
              class="den-approval-card-chip den-approval-card-chip--reason"
              data-testid={p.reasonChipTestid}
              data-tip={name()}
              data-tip-when-clipped=":scope > span"
            >
              <span>{name()}</span>
            </span>
          )}
        </Show>
        <Show when={p.chip}>
          {(name) => (
            <span
              class="den-approval-card-chip"
              data-testid={p.chipTestid}
              data-tip={name()}
              data-tip-when-clipped=":scope > span"
            >
              <span>{name()}</span>
            </span>
          )}
        </Show>
      </span>
      <Show when={p.path}>
        {(path) => (
          <span
            class="den-checkpoint-card-path"
            data-tip={path()}
            data-tip-when-clipped
          >
            {path()}
          </span>
        )}
      </Show>
    </div>
  );
}

function ElevatedApprovalChip(p: { tip: string }) {
  return <span
    class="den-approval-card-chip den-approval-card-chip--elevated"
    role="img"
    aria-label={p.tip}
    data-tip={p.tip}
    data-testid="approval-elevated-access"
  >
    <ThemeIcon slot="elevated-access" size={14} />
    <span>{APPROVALS_COPY.card.elevated.label}</span>
  </span>;
}

export type DetailRow = { label: string; value: string };

/** Keeps impact, agent rationale, and secondary details in a fixed order. */
export function ContextZone(p: {
  /** The one distinguishing fact, above the impact line. */
  lead?: string;
  impact?: string;
  impactTone?: "standard" | "risk";
  impactId?: string;
  impactTestid?: string;
  /** Host note shown beside the impact. */
  note?: string;
  noteTestid?: string;
  agent?: { text?: string; pending?: boolean; onceKey: string };
  detailRows?: DetailRow[];
  detailActions?: JSX.Element;
}) {
  const hasAgent = () => Boolean(p.agent && (p.agent.text?.trim() || p.agent.pending));
  const hasDetails = () =>
    (p.detailRows?.length ?? 0) > 0;
  const copy = APPROVALS_COPY.card;
  return (
    <Show when={p.lead || p.impact || p.note || hasAgent() || hasDetails()}>
      <div class="den-approval-card-context" data-zone="context">
        <Show when={p.lead?.trim()} keyed>
          {(text) => (
            <p class="den-approval-lead" data-testid="approval-lead">
              {text}
            </p>
          )}
        </Show>
        <Show when={p.impact} keyed>
          {(text) => (
            <p
              id={p.impactId}
              classList={{
                "den-approval-card-impact": true,
                "den-approval-card-impact--risk": p.impactTone === "risk",
              }}
              data-testid={p.impactTestid ?? "approval-impact"}
            >
              <ImpactIcon risk={p.impactTone === "risk"} />
              <span>{text}</span>
            </p>
          )}
        </Show>
        <Show when={p.note?.trim()} keyed>
          {(text) => (
            <p
              class="den-settings-hint den-approval-card-note"
              data-testid={p.noteTestid ?? "approval-note"}
            >
              {text}
            </p>
          )}
        </Show>
        <Show when={hasAgent()}>
          <div
            class="den-approval-card-agent"
            data-testid="approval-agent-line"
          >
            <span
              class="den-approval-card-agent-icon"
              data-tip={copy.agentLine.disclaimer}
              aria-hidden="true"
            >
              ✦
            </span>
            <span class="den-visually-hidden">{copy.agentLine.disclaimer}</span>
            <Show
              when={p.agent?.text?.trim()}
              keyed
              fallback={
                <span
                  class="den-approval-card-agent-pending"
                  data-testid="approval-ai-rationale-pending"
                  aria-label={copy.agentLine.pending}
                >
                  <span class="den-approval-card-agent-skeleton" />
                  <span class="den-approval-card-agent-skeleton den-approval-card-agent-skeleton--short" />
                </span>
              }
            >
              {(text) => (
                <span class="den-approval-card-agent-text">
                  <span class="den-approval-card-agent-label">
                    {copy.agentLine.label}:
                  </span>{" "}
                  <RandomLetterReveal
                    text={`“${text}”`}
                    onceKey={p.agent?.onceKey ?? ""}
                    class="den-approval-card-agent-body"
                    data-testid="approval-ai-rationale"
                  />
                </span>
              )}
            </Show>
          </div>
        </Show>
        <Show when={hasDetails()}>
          <ApprovalContentLink label="Approval details" pane="details"
            text={() => (p.detailRows ?? []).map(row => `${row.label}: ${row.value}`).join("\n\n")} />
        </Show>
        {p.detailActions}
      </div>
    </Show>
  );
}

/** Keeps refusal separate from the affirmative controls. */
export function ActionsZone(p: {
  no: { label: string; armedLabel?: string; onNo: (withGuidance: boolean) => void; testid?: string };
  sessionId?: string;
  middle?: JSX.Element;
  primary: {
    label: string;
    onPrimary: () => void;
    testid?: string;
    disabled?: boolean;
    title?: string;
    /** Coverage and expiry of the face option, under the row. */
    meta?: string;
  };
  resolving?: boolean;
}) {
  const armed = () => hasComposerGuidance(p.sessionId);
  return (
    <>
    <footer class="den-approval-card-actions" data-zone="actions">
      <DenButton
        variant="no"
        classList={{ "btn-no--armed": armed() }}
        data-testid={p.no.testid ?? "approval-no"}
        disabled={p.resolving}
        data-tip={APPROVALS_COPY.card.noAssurance}
        aria-keyshortcuts={armed() ? undefined : "Escape"}
        onClick={() => p.no.onNo(armed())}
      >
        <span>{armed() ? (p.no.armedLabel ?? APPROVALS_COPY.card.noArmed) : p.no.label}</span>
        <Show when={!armed()}>
          <kbd class="den-approval-kbd" aria-hidden="true">esc</kbd>
        </Show>
      </DenButton>
      <span class="den-approval-card-actions-affirm">
        {p.middle}
        <DenButton
          variant="primary"
          data-testid={p.primary.testid ?? "approval-approve-primary"}
          data-approval-primary=""
          disabled={p.resolving || p.primary.disabled}
          data-tip={p.primary.title}
          aria-keyshortcuts="Enter"
          onClick={p.primary.onPrimary}
        >
          <span>{p.primary.label}</span>
          <kbd class="den-approval-kbd" aria-hidden="true">↵</kbd>
        </DenButton>
      </span>
    </footer>
    <Show when={p.primary.meta} keyed>
      {(meta) => (
        <p class="den-approval-face-meta" data-testid="approval-face-meta">
          {meta}
        </p>
      )}
    </Show>
    </>
  );
}

export function RedirectRail(p: { sessionId?: string }) {
  const copy = APPROVALS_COPY.card.rail;
  const armed = () => hasComposerGuidance(p.sessionId);
  return (
    <button
      type="button"
      classList={{
        "den-approval-card-rail": true,
        "den-approval-card-rail--armed": armed(),
      }}
      data-zone="redirect-rail"
      data-testid="approval-redirect-rail"
      onClick={() => focusRegion("composer")}
    >
      <span class="den-approval-card-rail-icon" aria-hidden="true">
        ⤷
      </span>
      <Show
        when={armed()}
        fallback={
          <span>
            {copy.lead}{" "}
            <span class="den-approval-card-rail-link">{copy.typeBelow}</span>{" "}
            {copy.tail}
          </span>
        }
      >
        <span>{copy.armed}</span>
      </Show>
    </button>
  );
}

export function approvalKeyHandler(p: {
  enabled: () => boolean;
  onPrimary: () => void;
  onNo: () => void;
  onGrantMenu?: () => void;
  /** Digits 1–4 select the ladder rung in that slot. */
  onSlot?: (slot: number) => void;
  onEdit?: () => void;
}): (event: KeyboardEvent) => void {
  return (event) => {
    if (!p.enabled()) return;
    // An open menu handles Escape and arrow keys.
    if (
      event.target instanceof Element &&
      event.target.closest('[role="menu"]')
    ) {
      return;
    }
    // Text fields keep their key events.
    if (
      event.target instanceof HTMLElement &&
      (event.target.isContentEditable ||
        event.target.tagName === "INPUT" ||
        event.target.tagName === "TEXTAREA")
    ) {
      return;
    }
    const modified =
      event.metaKey || event.ctrlKey || event.altKey || event.shiftKey;
    if (!modified && p.onSlot && /^[1-4]$/.test(event.key)) {
      event.preventDefault();
      p.onSlot(Number(event.key));
      return;
    }
    switch (event.key) {
      case "Enter":
        if (modified) return;
        // Links and secondary controls keep their own Enter action.
        if (event.target instanceof Element) {
          const control = event.target.closest("button, a, summary, [role=button]");
          if (control && !control.hasAttribute("data-approval-primary")) return;
        }
        event.preventDefault();
        p.onPrimary();
        break;
      case "Escape":
        event.preventDefault();
        p.onNo();
        break;
      case "g":
        if (modified || !p.onGrantMenu) return;
        event.preventDefault();
        p.onGrantMenu();
        break;
      case "e":
        if (modified || !p.onEdit) return;
        event.preventDefault();
        p.onEdit();
        break;
    }
  };
}

function approvalTextPreview(text: string): string {
  const lines = text.split("\n", 5);
  const preview = lines.slice(0, 4).join("\n").slice(0, 320);
  return preview.length < text.length ? `${preview}…` : text;
}

export function SubjectBlock(p: { text: string; testid?: string; pane?: string }) {
  const preview = () => approvalTextPreview(p.text);
  return <>
    <pre class="den-approval-card-command" data-testid={p.testid ?? "approval-command"}>
      <code class="den-approval-card-command-code">{preview()}</code>
    </pre>
    <Show when={preview() !== p.text}>
      <ApprovalContentLink label="Full action" pane={p.pane ?? p.testid ?? "subject"} text={() => p.text} />
    </Show>
  </>;
}

export function SubjectFact(p: {
  label: string;
  value: string;
  testid: string;
}) {
  return (
    <p class="den-approval-card-subject-fact" data-testid={p.testid}>
      <span>{p.label}</span>
      <code>{p.value}</code>
    </p>
  );
}

export function SubjectLocation(p: {
  origin: string;
  destination: string;
  originKind: "file" | "field";
  destinationKind?: ApprovalSecretDestinationKind;
  onOrigin: () => void;
  testid?: string;
}) {
  const copy = APPROVALS_COPY.card;
  const receiver = () =>
    p.destinationKind ? copy.destinationKind[p.destinationKind] : undefined;
  return (
    <p class="den-approval-card-location" data-testid={p.testid ?? "approval-location"}>
      <button
        type="button"
        class="den-approval-card-location-origin"
        data-testid="approval-location-origin"
        data-origin-kind={p.originKind}
        onClick={p.onOrigin}
      >
        {p.origin}
      </button>
      <span class="den-approval-card-location-sep" aria-hidden="true">
        {copy.locationSeparator}
      </span>
      <span data-testid="approval-location-destination">{p.destination}</span>
      <Show when={receiver()} keyed>
        {(label) => (
          <span
            class="den-approval-card-chip"
            classList={{
              "den-approval-card-chip--reader":
                p.destinationKind === "model_provider",
            }}
            data-testid="approval-location-destination-kind"
            data-destination-kind={p.destinationKind}
          >
            {label}
          </span>
        )}
      </Show>
    </p>
  );
}

const INLINE_TARGET_LIMIT = 6;

function TargetList(p: { targets: ApprovalTarget[]; preview?: (target: ApprovalTarget) => string }) {
  return <div role="list" class="den-approval-card-declared" data-testid="approval-targets">
    <For each={p.targets}>{(target, index) => <div role="listitem" data-testid={`approval-target-${index()}`}>
      <SubjectBlock text={target.label} pane={`target:${index()}`} />
      <Show when={p.preview?.(target)} keyed>{preview => <SubjectBlock text={preview} pane={`target-preview:${index()}`} />}</Show>
    </div>}</For>
  </div>;
}

export function SubjectTargets(p: {
  title: string;
  kind?: string;
  targets: ApprovalTarget[];
  preview?: (target: ApprovalTarget) => string;
  sharedFact?: string;
}) {
  return (
    <>
      <Show
        when={p.targets.length > INLINE_TARGET_LIMIT}
        fallback={
          <TargetList targets={p.targets} preview={p.preview} />
        }
      >
        <SubjectBlock text={p.title} testid="approval-subject" />
        <ApprovalContentLink label={APPROVALS_COPY.card.targetSet.show(p.targets.length, p.kind)} title="Approval targets" pane="targets"
          text={() => p.targets.map((target, index) => {
            const preview = p.preview?.(target);
            return `${index + 1}. ${target.label}${preview ? `\n${preview}` : ""}`;
          }).join("\n\n")} />
      </Show>
      <Show when={p.sharedFact} keyed>
        {(fact) => (
          <p
            class="den-approval-card-declared-shared"
            data-testid="approval-targets-shared-fact"
          >
            {fact}
          </p>
        )}
      </Show>
    </>
  );
}

function HighRiskIcon() {
  return (
    <ThemeIcon
      slot="high-risk"
      size={12}
      class="den-approval-card-high-risk-icon"
    />
  );
}

function ImpactIcon(p: { risk: boolean }) {
  return (
    <ThemeIcon
      slot={p.risk ? "high-risk" : "impact"}
      size={13}
      class="den-approval-card-impact-icon"
    />
  );
}
