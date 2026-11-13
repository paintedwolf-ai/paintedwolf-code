import { Show, createEffect, createMemo, createSignal, on, onCleanup, untrack } from "solid-js";
import { EditorView } from "@codemirror/view";
import { Compartment, StateEffect } from "@codemirror/state";
import type { LycaonClient } from "../../api/client.ts";
import { chatContentDocumentKey, type ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import type { ChatContentMatch } from "../../chat/transcript/content/chat-content-reader.ts";
import { CommandReceiptLimitError } from "../../chat/transcript/content/command-receipt-access.ts";
import { useChatContentAccess } from "../../chat/transcript/content/use-chat-content-access.ts";
import { languageLabelForDetectedLanguage, PLAIN_TEXT_LABEL } from "../../components/source/editor/codemirror-lang.ts";
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
const changingFailure = {
  code: "chat_content_changing",
  title: "Content is still changing",
  suggestedAction: "Try again when the command finishes.",
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
  const [found, setFound] = createSignal<ChatContentMatch>();
  const [retainedText, setRetainedText] = createSignal<ChatRetainedText | null>(null);
  const [processSnapshot, setProcessSnapshot] = createSignal<{ text: string; rev: number } | null>(null);
  const copyAbort = new AbortController();
  onCleanup(() => copyAbort.abort());
  const reportError = (cause: unknown) => reportSurfaceFailure(contentFailure, cause, props.projectId);
  const reportCopyError = (cause: unknown) => reportSurfaceFailure(copyFailure, cause, props.projectId);
  const reportChanging = (cause: unknown) => reportSurfaceFailure(changingFailure, cause, props.projectId);
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
  const access = useChatContentAccess(() => props.document, () => props.client, reportError);
  let retainedGen = 0;
  createEffect(() => {
    const contentAccess = access();
    const enabled = prettyEnabled();
    const key = tabKey();
    contentAccess?.revision();
    const gen = ++retainedGen;
    setRetainedText(null);
    if (!contentAccess || !enabled) return;
    const abort = new AbortController();
    onCleanup(() => abort.abort());
    void contentAccess
      .prettySnapshot(abort.signal)
      .then((snap) => {
        if (abort.signal.aborted || gen !== retainedGen || !snap) return;
        if ("limit" in snap) {
          // The paged reader keeps the text; at the size limit Pretty says why it is not laid out.
          setPrettyLayoutOutcome(props.projectId, key, { kind: "limit", reason: "size", length: snap.limit });
          return;
        }
        if (!redactionSpansFit(snap.text, snap.spans)) return;
        setRetainedText({ text: snap.text, spans: snap.spans });
      })
      .catch((cause) => {
        // Other failures keep the protected paged view.
        if (cause instanceof CommandReceiptLimitError) reportChanging(cause);
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
    // A new revision replaces every row, so it gets a new editor.
    content.revision();
    const editor = new ChatContentEditor(parent, content, untrack(editorDisplayPrefs), reportError);
    setReader(editor);
    onCleanup(() => { editor.destroy(); setReader(undefined); });
  });
  const revealInInlinePretty = (match: ChatContentMatch) => {
    const view = inlineEditor();
    if (!view) return;
    const raw = view.state.doc.toString();
    let base = 0;
    if ("row" in match) for (let row = 0; row < match.row && base >= 0; row++) base = raw.indexOf("\n", base) + 1;
    const start = "row" in match ? runeOffsetToUtf16(raw.slice(base), match.column) + base : runeOffsetToUtf16(raw, match.offset);
    const end = match.length > 0 ? start + runeOffsetToUtf16(raw.slice(start), match.length) : start;
    view.dispatch({ selection: { anchor: start, head: end }, scrollIntoView: true });
  };
  const reveal = (match: ChatContentMatch) => {
    const editor = reader();
    if (editor && usePagedReader()) void editor.revealMatch(match);
    else revealInInlinePretty(match);
  };
  createEffect(() => {
    const target = props.document.revealOffset;
    if (target !== undefined && (reader() || inlineEditor())) reveal({ offset: target, length: 0 });
  });
  createEffect(() => { const prefs = editorDisplayPrefs(); reader()?.reader.prefs(prefs); });
  createEffect(on(
    () => [findController.isOpen(), findController.query()],
    () => { setFound(undefined); untrack(reader)?.clearMatch(); },
    { defer: true },
  ));
  createEffect(() => {
    const match = found();
    if (match !== undefined && findController.isOpen()) untrack(() => reveal(match));
  });
  bindChatContentFind({ access: () => interactive() ? access() : undefined, host, collapsed: () => false, expand: () => () => {}, reveal: setFound, onError: reportChanging });
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
    openGotoLine({ lineCount: Math.max(1, content.rows()),
      currentLine: (editor.reader.document.at(view.state.selection.main.head)?.slot.index ?? 0) + 1,
      focus: () => view.focus(), goTo: (line, column) => { void editor.revealRow(line - 1, column); },
    });
  };
  const copy = () => {
    const content = access();
    if ((props.document.content.kind === "retained" || props.document.content.kind === "command_receipts") && !content) {
      reportCopyError(new Error("Reconnect to copy the complete document."));
      return;
    }
    void (content ? content.copyAll(copyAbort.signal) : Promise.resolve(inlineEditor()?.state.doc.toString() ?? ""))
      .then(copyTextToClipboard).catch(reportCopyError);
  };
  createEffect(() => { props.onHandle?.({ gotoLine }); onCleanup(() => props.onHandle?.(undefined)); });
  const isProcessSnapshot = () => props.document.content.kind === "process" && prettyEnabled();
  // An unsettled receipt is a snapshot of records that are still being written.
  const isSnapshot = () => isProcessSnapshot() || access()?.settled() === false;
  const refreshSnapshot = () => {
    if (isProcessSnapshot()) refreshProcessSnapshot();
    else void access()?.refresh(copyAbort.signal).catch(reportError);
  };
  const languageLabel = () => props.document.language || props.document.path
    ? languageLabelForDetectedLanguage(props.document.language ?? "", props.document.path ?? "")
    : PLAIN_TEXT_LABEL;
  return <section ref={setHost} class="den-source-reader" data-testid="chat-content-page" aria-label={props.document.title}>
    <ReadonlyFilesDocument title={props.document.title}
      actions={<Show when={isSnapshot()}><button type="button" class="den-files-editor__tool den-inset-icon-btn" data-testid="chat-process-pretty-refresh" aria-label="Refresh snapshot" data-tip="Refresh snapshot" data-tip-pos="below" onClick={refreshSnapshot}>Refresh</button></Show>}
      onFind={() => { activeEditor()?.focus(); openFind(); }} onGotoLine={gotoLine} onCopy={copy}
      status={{ identity: resolveEditorIdentity({ historical: false, workerDraft: false, deleted: false, writable: null, preview: false, editable: false, unsupported: true }),
        windows: <DocumentWindows projectId={props.projectId} />,
        dirtyCount: projectFilesState(props.projectId).order.filter(key => projectFilesState(props.projectId).byKey[key]?.dirty).length,
        cursor: cursor(), language: languageLabel(), indent: { label: indentChipLabel(editorDisplayPrefs().indent) },
        details: <><Show when={isSnapshot()}><span class="den-files-editor__status-detail" data-testid="chat-process-pretty-snapshot">Snapshot</span></Show><span class="den-files-editor__status-detail" data-testid="files-editor-encoding-chip">UTF-8</span></>,
        pretty: prettyStatusToggleForTab(props.projectId, tabKey()),
      }}>
    <Show when={usePagedReader()} fallback={<InlineChatContent document={props.document} client={props.client} prettyEnabled={prettyEnabled()} processSnapshot={processSnapshot()} retainedText={retainedText()} onOutcome={(outcome) => setPrettyLayoutOutcome(props.projectId, tabKey(), outcome)} onError={reportError} onEditor={setInlineEditor} />}>
      <div class="den-source-reader__editors"><div class="den-source-reader__editor" ref={setEditorHost} /></div>
    </Show>
    </ReadonlyFilesDocument>
  </section>;
}

function InlineChatContent(props: {
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
