import { Index, Match, Show, Switch, createMemo, type JSX } from "solid-js";
import type { ActionLink, ActionOpener } from "../../api/types.ts";
import type { CardAction } from "../../chat/action/action-merge.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { openRunningStage } from "../../platform/navigation/open-running.ts";
import { openInSearch } from "../../search/search-nav.ts";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import { ToolContentLink } from "../tool/ToolContentLink.tsx";
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
  evidence?: JSX.Element;
};

/** Input, Output, Receipt and Evidence, then the opener; dragging the links attaches the output to chat. */
export function ActionLinks(props: Props) {
  const content = useToolContent();
  // A Find match inside this call's input or output opens its document at the match.
  const revealOffset = (kind: ActionLink["kind"]) => {
    if (content?.revealedCallId?.() !== props.part.toolCallId) return undefined;
    return kind === "input" ? content.argsOffset() : kind === "output" ? content.outputOffset() : undefined;
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
      <Index each={props.action.links}>
        {(link) => {
          // Descriptors arrive as new objects on every update; a link stays mounted while it opens the same record.
          const document = createMemo(() => linkContent(link(), props.part), undefined, { equals: sameShape });
          return (
            <Show when={document()} keyed>
              {(shown) => {
                const kind = link().kind;
                const messageId = kind === "input" ? props.part.argsMessageId ?? props.part.assistantMessageId : props.part.messageId;
                const language = kind === "receipt" ? "jsonl" : undefined;
                return (
                  <ToolContentLink short label={LINK_LABELS[kind]} testId={`action-link-${kind}`} projectId={props.projectId}
                    pane={`action:${props.part.id}:${kind}`} content={shown} language={language}
                    messageId={messageId} toolCallId={props.part.toolCallId} revealOffset={revealOffset(kind)} />
                );
              }}
            </Show>
          );
        }}
      </Index>
      {props.evidence}
      <Show when={opener()} keyed>
        {(opener) => <Opener opener={opener} part={props.part} projectId={props.projectId} rootRefs={props.rootRefs} />}
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
