import { Show, createEffect, createMemo, createSignal, on, onCleanup, untrack } from "solid-js";
import { EditorView } from "@codemirror/view";
import { Compartment, StateEffect } from "@codemirror/state";
import type { LycaonClient } from "../../api/client.ts";
import { chatContentDocumentKey, type ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { createChatContentAccess, exceedsPrettyInput } from "../../chat/transcript/content/chat-content-reader.ts";
import { bindChatContentFind } from "../../chat/transcript/content/chat-content-find.ts";
import { getBackgroundProcessSnapshot, prepareBackgroundProcessOutput, subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import { bindFindableView } from "../../find/use-findable-view.ts";
import { activateFindableViewForEditor, findController, openFind, setFindCaseSensitive, setFindQuery } from "../../find/find-controller.ts";
import { openGotoLine } from "../../find/goto-line-controller.ts";
import { createSourceEditorState, applyEditorDisplayPrefs } from "../../components/source/editor/codemirror-theme.ts";
import { editorDisplayPrefs } from "../../components/source/editor/editor-display-prefs.ts";
import { ChatContentEditor } from "../editor/chat-content-editor.ts";
import { ReadonlyFilesDocument } from "../editor/ReadonlyFilesDocument.tsx";
import {
  isPrettyDisplayEnabled,
  prettyStatusToggleForTab,
  registerPrettySurfaceCapability,
  setPrettyLayoutOutcome,
  togglePrettyDisplayPref,
} from "../editor/pretty-display-state.ts";
import {
  applyPrettyConfig,
  createPrettyExtension,
  type PrettyConfig,
  prettyOutcomeListener,
  prettyOutcomeOf,
} from "../../components/source/pretty/pretty-layout.ts";
import { prettyTargetFor } from "../../components/source/pretty/pretty-recognition.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { resolveEditorIdentity } from "../documents/editor-identity.ts";
import { indentChipLabel } from "../../components/source/editor/indent-detect.ts";
import { DocumentWindows } from "./DocumentWindows.tsx";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import { useCommandReceiptDocument } from "../documents/command-receipt-document.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import {
  buildRawRedactionExtension,
  type ChatRetainedText,
  extractInlineRedactionSpans,
  redactionSpansFit,
  runeOffsetToUtf16,
} from "./chat-content-pretty.ts";

const contentFailure = {
  code: "chat_content_unavailable",
  title: "Content unavailable",
  suggestedAction: "Reopen the content from the chat, or check the connection.",
};
const copyFailure = {
  code: "chat_content_copy_failed",
  title: "Couldn’t copy the content",
  suggestedAction: "Check the connection and copy again.",
};

export function ChatContentPage(props: { projectId: string; document: ChatContentDocument; client: LycaonClient | null; onHandle?: (handle: {gotoLine: () => void} | undefined) => void }) {
  const interactive = useResidentInteractive();
  const [host, setHost] = createSignal<HTMLElement | null>(null);
  const [editorHost, setEditorHost] = createSignal<HTMLDivElement>();
  const [found, setFound] = createSignal<number>();
  const [retainedText, setRetainedText] = createSignal<ChatRetainedText | null>(null);
  const [processSnapshot, setProcessSnapshot] = createSignal<{ text: string; rev: number } | null>(null);
  const copyAbort = new AbortController();
  onCleanup(() => copyAbort.abort());
  const reportError = (cause: unknown) => reportSurfaceFailure(contentFailure, cause, props.projectId);
  const reportCopyError = (cause: unknown) => reportSurfaceFailure(copyFailure, cause, props.projectId);
  const receipts = useCommandReceiptDocument(() => props.document, () => props.client, reportError);
  const tabKey = createMemo(() => chatContentDocumentKey(props.document));
  const prettyEnabled = () => isPrettyDisplayEnabled(props.projectId, tabKey());
  const layoutAvailable = () => prettyTargetFor(props.document.path, props.document.language) !== null;
  createEffect(() => {
    const key = tabKey();
    onCleanup(
      registerPrettySurfaceCapability(props.projectId, key, {
        enabled: () => isPrettyDisplayEnabled(props.projectId, key),
        available: layoutAvailable,
        onToggle: () => togglePrettyDisplayPref(props.projectId, key),
      }),
    );
  });
  const refreshProcessSnapshot = () => {
    if (props.document.content.kind !== "process") return;
    const snap = getBackgroundProcessSnapshot(props.document.sessionId, props.document.content.handle)?.text ?? "";
    setProcessSnapshot((prev) => ({ text: snap, rev: (prev?.rev ?? 0) + 1 }));
  };
  createEffect(() => {
    if (props.document.content.kind === "process" && prettyEnabled()) {
      untrack(refreshProcessSnapshot);
    } else {
      setProcessSnapshot(null);
    }
  });
  const retainedSource = createMemo<Parameters<typeof createChatContentAccess>[0] | undefined>(() => {
    const content = props.document.content;
    if (props.document.kind !== "tool" || content.kind !== "retained" || !props.client) return undefined;
    return { client: props.client, sessionId: props.document.sessionId,
      messageId: props.document.messageId, reference: content.reference };
  }, undefined, {
    equals: (previous, next) => previous === next || !!previous && !!next &&
      previous.client === next.client && previous.sessionId === next.sessionId &&
      previous.messageId === next.messageId && previous.reference.field === next.reference.field &&
      previous.reference.tool_call_id === next.reference.tool_call_id &&
      previous.reference.sha256 === next.reference.sha256,
  });
  const access = createMemo(() => {
    const source = retainedSource();
    return source ? createChatContentAccess(source) : undefined;
  });
  let retainedGen = 0;
  createEffect(() => {
    const contentAccess = access();
    const enabled = prettyEnabled();
    const key = tabKey();
    const gen = ++retainedGen;
    setRetainedText(null);
    if (!contentAccess || !enabled) return;
    const abort = new AbortController();
    onCleanup(() => abort.abort());
    void contentAccess
      .readBoundedSnapshot(abort.signal)
      .then((snap) => {
        if (abort.signal.aborted || gen !== retainedGen) return;
        if (!snap) {
          // The paged reader keeps showing it; Pretty reports when size is why.
          if (exceedsPrettyInput(contentAccess.reference)) {
            setPrettyLayoutOutcome(props.projectId, key, { kind: "limit", reason: "size", length: contentAccess.reference.total_runes });
          }
          return;
        }
        if (snap.sha256 !== contentAccess.reference.sha256 || !redactionSpansFit(snap.text, snap.spans)) return;
        setRetainedText({ text: snap.text, spans: snap.spans });
      })
      .catch(() => {
        /* Retained read failure keeps the protected raw ChatContentEditor view. */
      });
  });
  const [reader, setReader] = createSignal<ChatContentEditor>();
  const [inlineEditor, setInlineEditor] = createSignal<EditorView>();
  const [cursor, setCursor] = createSignal({ line: 1, col: 1 });
  const usePagedReader = () => Boolean(access() && !retainedText());
  const activeEditor = () => (usePagedReader() ? reader()?.reader.view : inlineEditor());
  createEffect(() => {
    const view = activeEditor();
    if (!view) return;
    const updateStatus = () => {
      const position = view.state.selection.main.head;
      const content = usePagedReader() ? reader()?.reader.document.at(position) : undefined;
      const line = view.state.doc.lineAt(position);
      setCursor({ line: content ? content.slot.index + 1 : line.number, col: position - line.from + 1 });
    };
    updateStatus();
    view.dispatch({ effects: StateEffect.appendConfig.of(EditorView.updateListener.of(update => {
      if (update.selectionSet || update.docChanged) updateStatus();
    })) });
  });
  createEffect(() => {
    const parent = editorHost(), content = access();
    if (!parent || !content || retainedText()) return;
    const editor = new ChatContentEditor(parent, content, untrack(editorDisplayPrefs), reportError);
    setReader(editor);
    onCleanup(() => { editor.destroy(); setReader(undefined); });
  });
  const revealInInlinePretty = (runeOffset: number, runeLength = 0) => {
    const view = inlineEditor();
    if (!view) return;
    const raw = view.state.doc.toString();
    const from = runeOffsetToUtf16(raw, runeOffset);
    const to = runeLength > 0 ? runeOffsetToUtf16(raw, runeOffset + runeLength) : from;
    view.dispatch({ selection: { anchor: from, head: to }, scrollIntoView: true });
  };
  createEffect(() => {
    const target = props.document.revealOffset, editor = reader();
    if (target === undefined) return;
    if (editor && usePagedReader()) void editor.reveal(target);
    else revealInInlinePretty(target);
  });
  createEffect(() => { const prefs = editorDisplayPrefs(); reader()?.reader.prefs(prefs); });
  createEffect(on(
    () => [findController.isOpen(), findController.query()],
    () => { setFound(undefined); untrack(reader)?.clearMatch(); },
    { defer: true },
  ));
  createEffect(() => {
    const offset = found();
    if (offset !== undefined && findController.isOpen()) {
      const len = Array.from(untrack(findController.query)).length;
      if (usePagedReader()) void reader()?.reveal(offset, len);
      else revealInInlinePretty(offset, len);
    }
  });
  bindChatContentFind({ access: () => interactive() ? access() : undefined, host, collapsed: () => false, expand: () => () => {}, reveal: setFound });
  bindFindableView({ id: chatContentDocumentKey(props.document), root: host,
    get provider() { return usePagedReader() ? "dom" : "codemirror"; }, getEditorView: activeEditor, primary: interactive, enabled: interactive });
  createEffect(() => {
    const request = props.document.find, view = inlineEditor();
    if (!request || !view || !interactive()) return;
    let active = true;
    onCleanup(() => { active = false; });
    queueMicrotask(() => {
      if (!active || !findController.isOpen() || !activateFindableViewForEditor(view)) return;
      setFindCaseSensitive(request.caseSensitive);
      openFind();
      const targetOffset = request.offset !== undefined ? runeOffsetToUtf16(view.state.doc.toString(), request.offset) : 0;
      view.dispatch({ selection: { anchor: Math.max(0, Math.min(view.state.doc.length, targetOffset)) } });
      setFindQuery(request.query);
    });
  });
  const gotoLine = () => {
    const editor = reader(), view = activeEditor(), content = access();
    if (!view) return;
    if (!usePagedReader() || !editor || !content) { openGotoLine(view); return; }
    openGotoLine({ lineCount: Math.max(1, content.reference.rows),
      currentLine: (editor.reader.document.at(view.state.selection.main.head)?.slot.index ?? 0) + 1,
      focus: () => view.focus(), goTo: (line, column) => { void editor.revealRow(line - 1, column); },
    });
  };
  const copy = () => {
    const content = access();
    if (props.document.content.kind === "command_receipts" && !receipts().ready) {
      reportCopyError(new Error("Wait for the complete execution records before copying."));
      return;
    }
    if (props.document.content.kind === "retained" && !content) {
      reportCopyError(new Error("Reconnect to copy the complete document."));
      return;
    }
    void (content ? content.text(0, content.reference.total_runes, copyAbort.signal) : Promise.resolve(inlineEditor()?.state.doc.toString() ?? ""))
      .then(copyTextToClipboard).catch(reportCopyError);
  };
  createEffect(() => { props.onHandle?.({ gotoLine }); onCleanup(() => props.onHandle?.(undefined)); });
  const isProcessSnapshot = () => props.document.content.kind === "process" && prettyEnabled();
  return <section ref={setHost} class="den-source-reader" data-testid="chat-content-page" aria-label={props.document.title}>
    <ReadonlyFilesDocument title={props.document.title}
      actions={<Show when={isProcessSnapshot()}><button type="button" class="den-files-editor__tool den-inset-icon-btn" data-testid="chat-process-pretty-refresh" aria-label="Refresh snapshot" data-tip="Refresh snapshot" data-tip-pos="below" onClick={refreshProcessSnapshot}>Refresh</button></Show>}
      onFind={() => { activeEditor()?.focus(); openFind(); }} onGotoLine={gotoLine} onCopy={copy}
      status={{ identity: resolveEditorIdentity({ historical: false, workerDraft: false, deleted: false, writable: null, preview: false, editable: false, unsupported: true }),
        windows: <DocumentWindows projectId={props.projectId} />,
        dirtyCount: projectFilesState(props.projectId).order.filter(key => projectFilesState(props.projectId).byKey[key]?.dirty).length,
        cursor: cursor(), language: "Plain text", indent: { label: indentChipLabel(editorDisplayPrefs().indent) },
        details: <><Show when={isProcessSnapshot()}><span class="den-files-editor__status-detail" data-testid="chat-process-pretty-snapshot">Snapshot</span></Show><span class="den-files-editor__status-detail" data-testid="files-editor-encoding-chip">UTF-8</span></>,
        pretty: prettyStatusToggleForTab(props.projectId, tabKey()),
      }}>
    <Show when={usePagedReader()} fallback={<InlineChatContent receiptText={receipts().text} document={props.document} client={props.client} prettyEnabled={prettyEnabled()} processSnapshot={processSnapshot()} retainedText={retainedText()} onOutcome={(outcome) => setPrettyLayoutOutcome(props.projectId, tabKey(), outcome)} onError={reportError} onEditor={setInlineEditor} />}>
      <div class="den-source-reader__editors"><div class="den-source-reader__editor" ref={setEditorHost} /></div>
    </Show>
    </ReadonlyFilesDocument>
  </section>;
}

function InlineChatContent(props: {
  receiptText: string;
  document: ChatContentDocument;
  client: LycaonClient | null;
  prettyEnabled: boolean;
  processSnapshot: { text: string; rev: number } | null;
  retainedText: ChatRetainedText | null;
  onOutcome: (outcome: ReturnType<typeof prettyOutcomeOf>) => void;
  onError: (cause: unknown) => void;
  onEditor: (editor: EditorView | undefined) => void;
}) {
  const [revision, setRevision] = createSignal(0);
  const [parent, setParent] = createSignal<HTMLDivElement>();
  const handle = () => props.document.content.kind === "process" ? props.document.content.handle : "";
  onCleanup(subscribeBackgroundProcessStore(() => setRevision(value => value + 1), () => ({ sessionId: props.document.sessionId, processId: handle() })));
  createEffect(() => {
    const client = props.client, process = handle();
    if (client && process) void prepareBackgroundProcessOutput(client, props.document.sessionId, process).catch(props.onError);
  });
  const rawText = () => {
    if (props.retainedText) return props.retainedText.text;
    const content = props.document.content;
    if (content.kind === "inline") return content.text;
    if (content.kind === "command_receipts") return props.receiptText;
    if (content.kind === "retained") return (content.reference.preview_rows ?? []).map(row => row.text).join("");
    if (content.kind === "process" && props.prettyEnabled && props.processSnapshot) return props.processSnapshot.text;
    revision();
    return content.kind === "process" ? getBackgroundProcessSnapshot(props.document.sessionId, content.handle)?.text ?? "" : "";
  };
  const redactionSpans = () => {
    if (props.retainedText) return props.retainedText.spans;
    const content = props.document.content;
    return content.kind === "inline" ? extractInlineRedactionSpans(content.redaction) : [];
  };
  const prettyConfig = (): PrettyConfig => ({
    enabled: props.prettyEnabled,
    ...(props.document.path ? { path: props.document.path } : {}),
    ...(props.document.language ? { languageHint: props.document.language } : {}),
  });
  let editor: EditorView | undefined;
  const redactions = new Compartment();
  createEffect(() => {
    const host = parent(); if (!host) return;
    const value = untrack(rawText);
    const view = new EditorView({ parent: host, state: createSourceEditorState({ surface: "diff-viewer", doc: value, ...untrack(editorDisplayPrefs), editable: false, scrollPastEnd: false, commandBridge: true,
      extensions: [
        redactions.of(buildRawRedactionExtension(value, untrack(redactionSpans))),
        createPrettyExtension(untrack(prettyConfig)),
        prettyOutcomeListener((outcome) => props.onOutcome(outcome)),
      ],
    }) });
    editor = view;
    props.onEditor(view);
    props.onOutcome(prettyOutcomeOf(view.state));
    onCleanup(() => { view.destroy(); if (editor === view) editor = undefined; props.onEditor(undefined); });
  });
  createEffect(() => {
    const value = rawText(), view = editor; if (!view) return;
    const effects = [redactions.reconfigure(buildRawRedactionExtension(value, redactionSpans()))];
    const previous = view.state.doc.toString();
    if (previous === value) view.dispatch({ effects });
    else {
      let from = 0, to = previous.length, end = value.length;
      while (from < to && from < end && previous[from] === value[from]) from++;
      while (to > from && end > from && previous[to - 1] === value[end - 1]) { to--; end--; }
      view.dispatch({ changes: { from, to, insert: value.slice(from, end) }, effects });
    }
    applyPrettyConfig(view, prettyConfig());
  });
  createEffect(() => { const prefs = editorDisplayPrefs(); if (editor) applyEditorDisplayPrefs(editor, prefs); });
  return <div class="den-source-reader__editors"><div class="den-source-reader__editor" ref={setParent} /></div>;
}
