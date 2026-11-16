import { For, Show, type JSX } from "solid-js";
import type { ActionLink, ActionOpener } from "../../api/types.ts";
import type { CardAction } from "../../chat/action/action-merge.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { openRunningStage } from "../../platform/navigation/open-running.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { openInSearch } from "../../search/search-nav.ts";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import { ToolContentLink } from "../tool/ToolContentLink.tsx";

const LINK_LABELS: Readonly<Record<ActionLink["kind"], string>> = { input: "Input", output: "Output", receipt: "Receipt" };

const OPENER_LABELS: Readonly<Record<ActionOpener["kind"], string>> = {
  file: "Open file", terminal: "Open terminal", running: "Open in Running", search: "Open in Search",
};

type Props = {
  action: CardAction;
  part: ToolPartView;
  projectId?: string;
  evidence?: JSX.Element;
};

/** Input, Output, Receipt and Evidence in that order, then the opener. */
export function ActionLinks(props: Props) {
  const content = useToolContent();
  // A Find match inside this call's input or output opens its document at the match.
  const revealOffset = (kind: ActionLink["kind"]) => {
    if (content?.revealedCallId?.() !== props.part.toolCallId) return undefined;
    return kind === "input" ? content.argsOffset() : kind === "output" ? content.outputOffset() : undefined;
  };
  return (
    <nav class="den-action-card__links" aria-label="Activity records" data-testid="action-links">
      <For each={props.action.links}>
        {(link) => {
          const content = linkContent(link, props.part);
          return (
            <Show when={content}>
              {(document) => (
                <ToolContentLink short label={LINK_LABELS[link.kind]} testId={`action-link-${link.kind}`} projectId={props.projectId}
                  pane={`action:${props.part.id}:${link.kind}`} content={document()} language={link.kind === "receipt" ? "jsonl" : undefined}
                  messageId={link.kind === "input" ? props.part.argsMessageId ?? props.part.assistantMessageId : props.part.messageId}
                  toolCallId={props.part.toolCallId} revealOffset={revealOffset(link.kind)} />
              )}
            </Show>
          );
        }}
      </For>
      {props.evidence}
      <Show when={props.action.opener}>
        {(opener) => <Opener opener={opener()} part={props.part} projectId={props.projectId} />}
      </Show>
    </nav>
  );
}

/** What a record link opens: a retained reference, the call's own text, a process, or receipts. */
function linkContent(link: ActionLink, part: ToolPartView): ChatContentDocument["content"] | undefined {
  switch (link.kind) {
    case "input":
      if (link.content_ref) return { kind: "retained", reference: link.content_ref };
      return { kind: "inline", text: JSON.stringify(part.args ?? {}), redaction: part.argsRedaction?.meta };
    case "output":
      if (link.process_handle) return { kind: "process", handle: link.process_handle };
      if (link.content_ref) return { kind: "retained", reference: link.content_ref };
      return part.output ? { kind: "inline", text: part.output, redaction: part.redaction ?? undefined } : undefined;
    case "receipt":
      return link.receipts?.length
        ? { kind: "command_receipts", scopes: link.receipts.map((scope) => ({ programId: scope.program_id, ...(scope.activity_id ? { activityId: scope.activity_id } : {}) })) }
        : undefined;
  }
}

function Opener(props: { opener: ActionOpener; part: ToolPartView; projectId?: string }) {
  const label = () => OPENER_LABELS[props.opener.kind];
  return (
    <Show when={props.opener.kind === "terminal" && props.opener.process_handle} fallback={
      <button type="button" class="den-chat-content-link den-action-card__opener" data-testid="action-opener" onClick={() => openDestination(props.opener, props.projectId)}>
        {label()}
      </button>
    }>
      {(handle) => (
        <span class="den-action-card__opener">
          <ToolContentLink short label={label()} testId="action-opener" projectId={props.projectId} pane={`action:${props.part.id}:terminal`}
            content={{ kind: "process", handle: handle() }} toolCallId={props.part.toolCallId} messageId={props.part.messageId} />
        </span>
      )}
    </Show>
  );
}

function openDestination(opener: ActionOpener, projectId: string | undefined): void {
  if (!projectId) return;
  switch (opener.kind) {
    case "file":
      if (opener.path) void openSourceLocation({ projectId, path: opener.path, line: opener.line, entryKind: "unknown", intent: "permanent" });
      return;
    case "running":
      openRunningStage({ projectId });
      return;
    case "search":
      if (opener.query) openInSearch(projectId, opener.query);
      return;
    case "terminal":
      return;
  }
}
