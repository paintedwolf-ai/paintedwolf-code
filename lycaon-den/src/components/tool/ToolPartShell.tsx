import { Show, createEffect, createMemo, createSignal, onCleanup, type JSX } from "solid-js";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { LycaonClient } from "../../api/client.ts";
import { createChatContentAccess } from "../../chat/transcript/content/chat-content-reader.ts";
import { bindChatContentFind } from "../../chat/transcript/content/chat-content-find.ts";
import { ToolContentProvider } from "../../chat/tool/tool-content.tsx";
import { TOOL_CONTENT_REVEAL_EVENT } from "../../chat/tool/tool-part-find.ts";
import { findController } from "../../find/find-controller.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { useTranscriptDisclosure } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
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
  const {
    key: disclosureKey,
    open,
    onToggle,
    onSummaryClick,
    applyOpen,
    revealTemporarily,
  } = useTranscriptDisclosure(() => transcriptDisclosureKey.tool(props.part.id));
  // Process-exit SSE updates the mirror without rewriting the tool_result row.
  const [processRevision, setProcessRevision] = createSignal(0);
  onCleanup(
    subscribeBackgroundProcessStore(() => {
      setProcessRevision((n) => n + 1);
    }, () => ({ sessionId: props.sessionId, processId: props.part.process?.handle, statusOnly: true })),
  );
  const summary = () => {
    processRevision();
    return toolSummaryFields(props.part, props.sessionId);
  };
  const toolName = () => props.name?.trim() || props.part.tool;
  const toolLabel = () => formatToolLabel(toolName());
  const accessibleSummary = () => {
    const title = summary().title;
    const target = title !== toolName() && title !== toolLabel() ? title : "";
    return [toolLabel(), target, statusDot(summary().status)].filter(Boolean).join(", ");
  };
  const completion = () => toolCompletionLabel(props.part);
  const [hostEl, setHostEl] = createSignal<HTMLElement | null>(null);
  const [summaryEl, setSummaryEl] = createSignal<HTMLElement | null>(null);
  const findId = () =>
    `tool-body:${props.part.toolCallId || props.part.id}`;
  const revealId = () =>
    `tool-reveal:${props.part.toolCallId || props.part.id}`;

  // Activity-span membership stays fixed for this mount.
  const accordion = useActivitySpanAccordion();
  if (accordion) {
    onCleanup(
      accordion.register({
        key: props.part.id,
        disclosureKey: transcriptDisclosureKey.tool(props.part.id),
        details: () => {
          const el = hostEl();
          return el instanceof HTMLDetailsElement ? el : null;
        },
        summary: summaryEl,
        applyOpen,
      }),
    );
  }

  const [outputOffset, setOutputOffset] = createSignal<number>();
  const [argsOffset, setArgsOffset] = createSignal<number>();
  let restoreContentFind: (() => void) | undefined;
  createEffect(() => {
    void findController.query(); void findController.isOpen();
    restoreContentFind?.(); restoreContentFind = undefined;
  });
  onCleanup(() => restoreContentFind?.());
  createEffect(() => {
    const host = hostEl(); if (!host) return;
    const reveal = (event: Event) => {
      const match = (event as CustomEvent<{ field?: string; from: number }>).detail;
      restoreContentFind?.(); restoreContentFind = revealTemporarily();
      if (match.field === "tool_output") setOutputOffset(match.from);
      if (match.field === "tool_args") setArgsOffset(match.from);
    };
    host.addEventListener(TOOL_CONTENT_REVEAL_EVENT, reveal);
    onCleanup(() => host.removeEventListener(TOOL_CONTENT_REVEAL_EVENT, reveal));
  });
  const contentAccess = (field: "output" | "args") => createMemo(() => {
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
    collapsedCorpus: () => [props.part.title, props.part.displaySubject, props.part.argsReference ? "" : JSON.stringify(props.part.args), props.part.outputReference ? "" : props.part.output].filter(Boolean).join("\n"),
    collapsedCorpusAnchor: summaryEl,
  });

  const handleSummaryClick: JSX.EventHandler<HTMLElement, MouseEvent> = (
    event,
  ) => {
    if (!accordion) {
      onSummaryClick(event);
      return;
    }
    // The accordion controls the paired row transition.
    event.preventDefault();
    accordion.toggle(props.part.id);
  };

  return (
    <details
      ref={(el) => {
        bindTranscriptEntry(el);
        setHostEl(el);
      }}
      class="den-tool-part-card den-transcript-disclosure-card den-tool-part"
      data-testid={props.testId ?? "tool-part-card"}
      data-tool={props.part.tool}
      data-tool-call-id={props.part.toolCallId}
      data-invocation-owner={props.part.invocation?.owner}
      data-invocation-lifecycle={props.part.invocation?.lifecycle}
      data-invocation-status={props.part.invocation?.status}
      data-layout={props.layout}
      data-status={statusDot(summary().status)}
      data-disclosure-key={disclosureKey}
      data-accordion-row={accordion ? "" : undefined}
      open={open()}
      onToggle={onToggle}
    >
      <TranscriptChickletSummary
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
          <span class="den-tool-part-title">{summary().title}</span>
        </Show>
        <Show when={completion()}>
          {(label) => <span class="den-tool-part-title">{label()}</span>}
        </Show>
      </TranscriptChickletSummary>
      <div
        class="den-tool-part-card-body den-tool-part-body"
        data-testid="tool-part-body"
      >
        <ToolContentProvider value={{ output: outputAccess, args: argsAccess, outputOffset, argsOffset,
          identity: () => ({ projectId: props.projectId, sessionId: props.sessionId ?? "", messageId: props.part.messageId,
            toolCallId: props.part.toolCallId ?? props.part.id, title: `${toolLabel()} · ${props.part.title ?? ""}` }),
        }}>
          <Show when={open()}>{props.children}</Show>
        </ToolContentProvider>
      </div>
    </details>
  );
}
