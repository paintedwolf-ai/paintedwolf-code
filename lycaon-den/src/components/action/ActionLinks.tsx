import { Index, Match, Show, Switch, createMemo } from "solid-js";
import type { ActionLink, ActionOpener } from "../../api/types.ts";
import type { CardAction } from "../../chat/action/action-merge.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { openRunningStage } from "../../platform/navigation/open-running.ts";
import { openInSearch } from "../../search/search-nav.ts";
import { turnLoadRowSummary } from "../../chat/turnload/turn-load-rows.ts";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import { ToolContentLink, toolContentDocument } from "../tool/ToolContentLink.tsx";
import type { BriefingEvidenceRef } from "../../api/types.ts";
import { BriefingEvidenceLink } from "../../briefing/BriefingSummary.tsx";
import type { BriefingRecordLink } from "../../briefing/briefing-evidence.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import { SourcePathLink } from "../source/SourcePathLink.tsx";
import { TOOL_PATH_ENTRY_KINDS } from "../../chat/tool/tool-presentation.generated.ts";
import { chatRefForToolRaw } from "../../chat/composer/search-add-to-chat.ts";
import { startChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";

const LINK_LABELS: Readonly<Record<ActionLink["kind"], string>> = { input: "Input", output: "Output", receipt: "Receipt" };

const OPENER_LABELS: Readonly<Record<ActionOpener["kind"], string>> = {
  file: "Open in Files", terminal: "Open terminal", running: "Open in Running", search: "Open in Search",
};

type Props = {
  action: CardAction;
  part: ToolPartView;
  projectId?: string;
  sessionId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  evidence?: BriefingEvidenceRef;
};

type RecordLink = { kind: ActionLink["kind"]; pane: string; content: ChatContentDocument["content"]; language?: string; messageId?: string };

/** What one record link opens; the evidence page offers the same documents. */
function recordLink(link: ActionLink, part: ToolPartView): RecordLink | undefined {
  const content = linkContent(link, part);
  if (!content) return undefined;
  return {
    kind: link.kind, pane: `action:${part.id}:${link.kind}`, content,
    language: link.kind === "receipt" ? "jsonl" : undefined,
    messageId: link.kind === "input" ? part.argsMessageId ?? part.assistantMessageId : part.messageId,
  };
}

/** Dragging the records attaches the call's output to chat. */
export function ActionLinks(props: Props) {
  const content = useToolContent();
  // A Find match inside this call's input or output opens its document at the match.
  const revealOffset = (kind: ActionLink["kind"]) => {
    if (content?.revealedCallId?.() !== props.part.toolCallId) return undefined;
    return kind === "input" ? content.argsOffset() : kind === "output" ? content.outputOffset() : undefined;
  };
  const links = (): readonly ActionLink[] => {
    const links = props.action.links;
    return props.part.commandActivity?.receiptScopes.length && !links.some(link => link.kind === "receipt")
      ? [...links, { kind: "receipt" }]
      : links;
  };
  // The evidence page offers this call's documents as the card does, in the same tabs.
  const evidenceRecords = (): BriefingRecordLink[] => {
    const identity = content?.identity();
    if (!identity) return [];
    return links().flatMap((link) => {
      const record = recordLink(link, props.part);
      if (!record) return [];
      const label = LINK_LABELS[record.kind];
      return [{ label, document: toolContentDocument(identity, { ...record, label, toolCallId: props.part.toolCallId }) }];
    });
  };
  const opener = createMemo(() => props.action.opener, undefined, { equals: sameShape });
  // The call's output as a chat attachment, citing its session and call.
  const outputRef = createMemo(() => chatRefForToolRaw({
    projectId: props.projectId?.trim() ?? "", sessionId: props.sessionId?.trim() ?? "",
    toolCallId: props.part.toolCallId?.trim() ?? "", name: props.part.tool.trim() || props.part.toolCallId,
  }));
  return (
    <nav class="den-action-card__links" aria-label="Activity records" data-testid="action-links"
      draggable={outputRef() ? true : undefined} onDragStart={(event) => startChatAttachmentDrag(event, outputRef())}>
      <Index each={links()}>
        {(link) => {
          // Descriptors arrive as new objects on every update; a link stays mounted while it opens the same record.
          const record = createMemo(() => recordLink(link(), props.part), undefined, { equals: sameShape });
          return (
            <Show when={record()} keyed>
              {(shown) => (
                <ToolContentLink short label={LINK_LABELS[shown.kind]} testId={`action-link-${shown.kind}`} projectId={props.projectId}
                  pane={shown.pane} content={shown.content} language={shown.language}
                  messageId={shown.messageId} toolCallId={props.part.toolCallId} revealOffset={revealOffset(shown.kind)} />
              )}
            </Show>
          );
        }}
      </Index>
      <Show when={hostDecisions(props.part)} keyed>
        {(text) => (
          <ToolContentLink short label="Host decisions" testId="command-activity-decisions"
            projectId={props.projectId} pane={`action:${props.part.id}:decisions`}
            content={{ kind: "inline", text }} />
        )}
      </Show>
      <Show when={props.evidence} keyed>{(reference) => <BriefingEvidenceLink reference={reference} records={evidenceRecords} />}</Show>
      <Show when={opener()} keyed>
        {(opener) => <Opener opener={opener} part={props.part} projectId={props.projectId} rootRefs={props.rootRefs} />}
      </Show>
    </nav>
  );
}

function hostDecisions(part: ToolPartView): string | undefined {
  const decisions = [part, ...(part.foldMembers ?? [])].flatMap(member => member.commandActivity?.decisions ?? []);
  if (!decisions.length) return undefined;
  return decisions.map(decision => {
    const summary = turnLoadRowSummary(decision.row);
    return `${summary.title} · ${summary.operation} · ${summary.state}`;
  }).join("\n");
}

function linkContent(link: ActionLink, part: ToolPartView): ChatContentDocument["content"] | undefined {
  switch (link.kind) {
    case "input":
      if (link.content_ref) return { kind: "retained", reference: link.content_ref };
      return { kind: "inline", text: JSON.stringify(part.args ?? {}), redaction: part.argsRedaction?.meta };
    case "output":
      if (link.process_handle) return { kind: "process", handle: link.process_handle };
      if (link.content_ref) return { kind: "retained", reference: link.content_ref };
      return part.output ? { kind: "inline", text: part.output, redaction: part.redaction ?? undefined } : undefined;
    case "receipt": {
      const scopes = part.commandActivity?.receiptScopes.length
        ? part.commandActivity.receiptScopes
        : link.receipts?.map(scope => ({ programId: scope.program_id, ...(scope.activity_id ? { activityId: scope.activity_id } : {}) }));
      return scopes?.length ? { kind: "command_receipts", scopes } : undefined;
    }
  }
}

function Opener(props: { opener: ActionOpener; part: ToolPartView; projectId?: string; rootRefs?: readonly ResolveProjectRoot[] }) {
  const label = () => OPENER_LABELS[props.opener.kind];
  return (
    <Switch fallback={
      <button type="button" class="den-chat-content-link den-action-card__opener" data-testid="action-opener" onClick={() => openDestination(props.opener, props.projectId)}>
        {label()}
      </button>
    }>
      <Match when={props.opener.kind === "file" && props.opener.path ? props.opener.path : undefined} keyed>
        {(path) => (
          <span class="den-action-card__opener" data-testid="action-opener">
            {/* The path resolves against the project's roots, so @label/ paths and folders open where they live. */}
            <SourcePathLink projectId={props.projectId ?? ""} path={path} line={props.opener.line} label={label()}
              entryKind={TOOL_PATH_ENTRY_KINDS[props.part.tool.toLowerCase()] ?? "unknown"} rootRefs={props.rootRefs} />
          </span>
        )}
      </Match>
      <Match when={props.opener.kind === "terminal" && props.opener.process_handle ? props.opener.process_handle : undefined} keyed>
        {(handle) => (
          <span class="den-action-card__opener">
            <ToolContentLink short label={label()} testId="action-opener" projectId={props.projectId} pane={`action:${props.part.id}:terminal`}
              content={{ kind: "process", handle }} toolCallId={props.part.toolCallId} messageId={props.part.messageId} />
          </span>
        )}
      </Match>
    </Switch>
  );
}

function openDestination(opener: ActionOpener, projectId: string | undefined): void {
  if (!projectId) return;
  switch (opener.kind) {
    case "running":
      openRunningStage({ projectId });
      return;
    case "search":
      if (opener.query) openInSearch(projectId, opener.query);
      return;
    case "file":
    case "terminal":
      return;
  }
}

/** Equal when two values describe the same record or destination. */
function sameShape(a: unknown, b: unknown): boolean {
  return a === b || (a !== undefined && b !== undefined && JSON.stringify(a) === JSON.stringify(b));
}
