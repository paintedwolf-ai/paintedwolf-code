import { bindVirtualListFind } from "../../find/virtual-list-find.ts";
import { Show, createEffect, createMemo, createSignal, onCleanup, type JSX } from "solid-js";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { LycaonClient } from "../../api/client.ts";
import { createChatContentAccess } from "../../chat/transcript/content/chat-content-reader.ts";
import { bindChatContentFind } from "../../chat/transcript/content/chat-content-find.ts";
import { ToolContentProvider } from "../../chat/tool/tool-content.tsx";
import { TOOL_CONTENT_REVEAL_EVENT, searchToolPart, toolPartFindCorpus } from "../../chat/tool/tool-part-find.ts";
import { findController } from "../../find/find-controller.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import {
  TranscriptDisclosurePanel,
  TranscriptDisclosureShell,
  useTranscriptDisclosure,
} from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { prepareBackgroundProcessOutput, subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import { useActivitySpanAccordion } from "../../chat/tool/activity-span-accordion.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type {
  ToolPartStatus,
  ToolPartView,
} from "../../chat/tool/tool-part-model.ts";
import {
  toolCompletionLabel,
  toolSummaryFields,
  toolPartDisplayStatus,
} from "../../chat/tool/tool-part-model.ts";
import {
  bindFindableView,
  bindFindRevealHost,
} from "../../find/use-findable-view.ts";
import { useTranscriptViewport } from "../../chat/stream/transcript-viewport.tsx";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

import { TranscriptChickletSummary } from "../transcript/TranscriptChickletSummary.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  client?: LycaonClient | null;
  testId?: string;
  /** Display name override for specialized cards. */
  name?: string;
  children: JSX.Element;
};

const TOOL_LABELS: Readonly<Record<string, string>> = {
  http_request: "HTTP request",
};

function formatToolLabel(name: string): string {
  return TOOL_LABELS[name.toLowerCase()] ?? formatSentenceCase(name);
}

/** Also the spoken word in the row's aria-label. */
function statusDot(status: ToolPartStatus): "done" | "error" | "running" {
  return status === "completed" ? "done" : status;
}

export function ToolPartShell(props: Props) {
  createEffect(() => {
    const client = props.client ?? getLycaonClient(); const sessionId = props.sessionId; const handle = props.part.process?.handle;
    if (client && sessionId && handle) void prepareBackgroundProcessOutput(client, sessionId, handle).catch(() => {});
  });
  const viewport = useTranscriptViewport();
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId,
    entryKey: props.part.id,
  }));
  const disclosure = useTranscriptDisclosure(() => transcriptDisclosureKey.tool(props.part.id));
  const { open, onToggleClick, applyOpen, revealTemporarily } = disclosure;
  // Process-exit SSE updates the mirror without rewriting the tool_result row.
  const [processRevision, setProcessRevision] = createSignal(0);
  onCleanup(
    subscribeBackgroundProcessStore(() => {
      setProcessRevision((n) => n + 1);
    }, () => ({ sessionId: props.sessionId, processId: props.part.process?.handle, statusOnly: true })),
  );
  const summary = () => {
    processRevision();
    const activity = props.part.commandActivity;
    return activity ? { status: toolPartDisplayStatus(props.part, props.sessionId), title: activity.fact.subject + (activity.outputReads ? ` · ${activity.outputReads} output reads` : activity.members.length > 1 ? ` · ${activity.members.length} reads` : "") } : toolSummaryFields(props.part, props.sessionId);
  };
  const toolName = () => props.name?.trim() || props.part.commandActivity?.fact.label || props.part.tool;
  const toolLabel = () => formatToolLabel(toolName());
  const accessibleSummary = () => {
    const title = summary().title;
    const target = title !== toolName() && title !== toolLabel() ? title : "";
    return [toolLabel(), target, props.part.commandActivity ? completion() : undefined, statusDot(summary().status)].filter(Boolean).join(", ");
  };
  const completion = () => { processRevision(); return toolCompletionLabel(props.part, props.sessionId); };
  const [hostEl, setHostEl] = createSignal<HTMLElement | null>(null);
  const [summaryEl, setSummaryEl] = createSignal<HTMLElement | null>(null);
  const findId = () =>
    `tool-body:${props.part.id}`;
  const revealId = () =>
    `tool-reveal:${props.part.id}`;

  // Activity-span membership stays fixed for this mount.
  const accordion = useActivitySpanAccordion();
  if (accordion) {
    onCleanup(
      accordion.register({
        key: props.part.id,
        disclosureKey: transcriptDisclosureKey.tool(props.part.id),
        shell: hostEl,
        header: summaryEl,
        applyOpen,
      }),
    );
  }

  const [outputOffset, setOutputOffset] = createSignal<number>();
  const [revealedCallId, setRevealedCallId] = createSignal<string>();
  const [argsOffset, setArgsOffset] = createSignal<number>();
  let restoreContentFind: (() => void) | undefined;
  createEffect(() => {
    void findController.query(); void findController.isOpen();
    restoreContentFind?.(); restoreContentFind = undefined;
    setOutputOffset(undefined); setArgsOffset(undefined); setRevealedCallId(undefined);
  });
  onCleanup(() => restoreContentFind?.());
  createEffect(() => {
    const host = hostEl(); if (!host) return;
    const reveal = (event: Event) => {
      const match = (event as CustomEvent<{ field?: string; from: number; toolCallId?: string }>).detail;
      restoreContentFind?.(); restoreContentFind = revealTemporarily();
      setRevealedCallId(match.toolCallId ?? props.part.toolCallId);
      if (match.field === "tool_output") setOutputOffset(match.from);
      if (match.field === "tool_args") setArgsOffset(match.from);
    };
    host.addEventListener(TOOL_CONTENT_REVEAL_EVENT, reveal);
    onCleanup(() => host.removeEventListener(TOOL_CONTENT_REVEAL_EVENT, reveal));
  });
  const contentAccess = (field: "output" | "args") => createMemo(() => {
    if (props.part.commandActivity) return undefined;
    const client = props.client ?? getLycaonClient();
    const reference = field === "output" ? props.part.outputReference : props.part.argsReference;
    if (!client || !props.sessionId || !reference) return undefined;
    return createChatContentAccess({ client, sessionId: props.sessionId,
      messageId: field === "output" ? props.part.messageId : props.part.argsMessageId ?? props.part.assistantMessageId,
      reference,
    });
  });
  const outputAccess = contentAccess("output");
  const argsAccess = contentAccess("args");
  bindChatContentFind({ access: outputAccess, host: hostEl, collapsed: () => !open(), expand: revealTemporarily, reveal: setOutputOffset });
  bindChatContentFind({ access: argsAccess, host: hostEl, collapsed: () => !open(), expand: revealTemporarily, reveal: setArgsOffset });

  // Small unmounted bodies use the data corpus; retained bodies use host search.
  bindFindableView({
    id: findId(),
    root: hostEl,
    enabled: () => !props.part.commandActivity,
    scrollMatchIntoView: (match) => {
      const node = match.range.startContainer;
      const element = node instanceof HTMLElement ? node : node.parentElement;
      if (element) viewport?.ensureVisible(element);
    },
  });
  bindFindRevealHost({
    id: revealId(),
    hostEl,
    isCollapsed: () => !open(),
    revealForFind: revealTemporarily,
    collapsedCorpus: () => props.part.commandActivity ? "" : toolPartFindCorpus(props.part),
    collapsedCorpusAnchor: summaryEl,
  });

  bindVirtualListFind({
    items: () => [props.part], host: () => hostEl() ?? undefined,
    enabled: () => !!props.part.commandActivity,
    search: (part, query, sensitive, signal) => searchToolPart(part, props.sessionId, query, sensitive, signal),
    reveal: (_index, match) => hostEl()?.dispatchEvent(new CustomEvent(TOOL_CONTENT_REVEAL_EVENT, { detail: match })),
  });

  const handleSummaryClick: JSX.EventHandler<HTMLButtonElement, MouseEvent> = (
    event,
  ) => {
    // The accordion controls the paired row transition.
    if (accordion) accordion.toggle(props.part.id);
    else onToggleClick(event);
  };

  return (
    <TranscriptDisclosureShell
      disclosure={disclosure}
      ref={(el) => {
        bindTranscriptEntry(el);
        setHostEl(el);
      }}
      class="den-tool-part-card den-transcript-disclosure-card den-tool-part"
      data-testid={props.testId ?? "tool-part-card"}
      data-tool={props.part.tool}
      data-command-activity={props.part.commandActivity ? "" : undefined}
      data-tool-call-id={props.part.toolCallId}
      data-tool-part-id={props.part.id}
      data-invocation-owner={props.part.invocation?.owner}
      data-invocation-lifecycle={props.part.invocation?.lifecycle}
      data-invocation-status={props.part.invocation?.status}
      data-layout={props.layout}
      data-status={statusDot(summary().status)}
      data-accordion-row={accordion ? "" : undefined}
    >
      <TranscriptChickletSummary
        disclosure={disclosure}
        ref={setSummaryEl}
        onClick={handleSummaryClick}
        label={accessibleSummary()}
        accordion={!!accordion}
      >
        <span
          class="den-tool-part-status-dot"
          data-status={statusDot(summary().status)}
          aria-hidden="true"
        />
        <span class="den-tool-part-name">{toolLabel()}</span>
        <Show
          when={
            summary().title &&
            summary().title !== toolName() &&
            summary().title !== toolLabel()
          }
        >
          <span class="den-tool-part-title" data-tip={summary().title} data-tip-when-clipped="">{summary().title}</span>
        </Show>
        <Show when={completion()}>
          {(label) => <span data-tip={label()} data-tip-when-clipped="" class={props.part.commandActivity ? "den-command-activity-outcome" : "den-tool-part-title"}>{label()}</span>}
        </Show>
      </TranscriptChickletSummary>
      <TranscriptDisclosurePanel
        disclosure={disclosure}
        class="den-tool-part-card-body den-tool-part-body"
        data-testid="tool-part-body"
      >
        <ToolContentProvider value={{ output: outputAccess, args: argsAccess, outputOffset, argsOffset, revealedCallId,
          identity: () => ({ projectId: props.projectId, sessionId: props.sessionId ?? "", messageId: props.part.messageId,
            toolCallId: props.part.toolCallId ?? props.part.id, title: `${toolLabel()} · ${props.part.title ?? ""}` }),
        }}>
          <Show when={open()}>{props.children}</Show>
        </ToolContentProvider>
      </TranscriptDisclosurePanel>
    </TranscriptDisclosureShell>
  );
}
