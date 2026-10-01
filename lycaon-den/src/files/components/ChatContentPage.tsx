import { Show, createEffect, createMemo, createSignal, on, onCleanup, untrack } from "solid-js";
import { Decoration, EditorView } from "@codemirror/view";
import { Compartment, StateEffect } from "@codemirror/state";
import type { LycaonClient } from "../../api/client.ts";
import type { HostSecretRedactionMeta } from "../../api/types.ts";
import { chatContentDocumentKey, type ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { createChatContentAccess } from "../../chat/transcript/content/chat-content-reader.ts";
import { bindChatContentFind } from "../../chat/transcript/content/chat-content-find.ts";
import { getBackgroundProcessSnapshot, prepareBackgroundProcessOutput, subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import { bindFindableView } from "../../find/use-findable-view.ts";
import { activateFindableViewForEditor, findController, openFind, setFindCaseSensitive, setFindQuery } from "../../find/find-controller.ts";
import { openGotoLine } from "../../find/goto-line-controller.ts";
import { createSourceEditorState, applyEditorDisplayPrefs } from "../../components/source/editor/codemirror-theme.ts";
import { editorDisplayPrefs } from "../../components/source/editor/editor-display-prefs.ts";
import { redactionMarkModifier, redactionMarkTitle } from "../../chat/transcript/content/redaction-spans.ts";
import { ChatContentEditor } from "../editor/chat-content-editor.ts";
import { ReadonlyFilesDocument } from "../editor/ReadonlyFilesDocument.tsx";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { resolveEditorIdentity } from "../documents/editor-identity.ts";
import { indentChipLabel } from "../../components/source/editor/indent-detect.ts";
import { DocumentWindows } from "./DocumentWindows.tsx";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";

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
  const interactive=useResidentInteractive();
  const [host, setHost] = createSignal<HTMLElement | null>(null);
  const [editorHost, setEditorHost] = createSignal<HTMLDivElement>();
  const [found, setFound] = createSignal<number>();
  const copyAbort = new AbortController();
  onCleanup(() => copyAbort.abort());
  const reportError = (cause: unknown) => reportSurfaceFailure(contentFailure, cause, props.projectId);
  const reportCopyError = (cause: unknown) => reportSurfaceFailure(copyFailure, cause, props.projectId);
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
  const [reader, setReader] = createSignal<ChatContentEditor>();
  const [inlineEditor, setInlineEditor] = createSignal<EditorView>();
  const [cursor,setCursor] = createSignal({line:1,col:1});
  const activeEditor = () => reader()?.reader.view ?? inlineEditor();
  createEffect(()=>{
    const view=activeEditor(); if(!view) return;
    const updateStatus=()=>{
      const position=view.state.selection.main.head;
      const content=reader()?.reader.document.at(position);
      const line=view.state.doc.lineAt(position);
      setCursor({line:content ? content.slot.index+1 : line.number,col:position-line.from+1});
    };
    updateStatus();
    view.dispatch({effects:StateEffect.appendConfig.of(EditorView.updateListener.of(update=>{
      if(update.selectionSet || update.docChanged) updateStatus();
    }))});
  });
  createEffect(() => {
    const parent = editorHost(), content = access();
    if (!parent || !content) return;
    const editor = new ChatContentEditor(parent,content,untrack(editorDisplayPrefs),reportError);
    setReader(editor);
    onCleanup(()=>{ editor.destroy(); setReader(undefined); });
  });
  createEffect(()=>{ const target=props.document.revealOffset, editor=reader(); if(target!==undefined) void editor?.reveal(target); });
  createEffect(()=>{ const prefs=editorDisplayPrefs(); reader()?.reader.prefs(prefs); });
  createEffect(on(
    () => [findController.isOpen(), findController.query()],
    () => { setFound(undefined); untrack(reader)?.clearMatch(); },
    { defer: true },
  ));
  createEffect(() => {
    const offset = found();
    if (offset !== undefined && findController.isOpen()) {
      void reader()?.reveal(offset, Array.from(untrack(findController.query)).length);
    }
  });
  bindChatContentFind({ access:()=>interactive() ? access() : undefined, host, collapsed: () => false, expand: () => () => {}, reveal: setFound });
  bindFindableView({ id: chatContentDocumentKey(props.document), root: host,
    get provider() { return access() ? "dom" : "codemirror"; }, getEditorView: activeEditor, primary: interactive, enabled: interactive });
  createEffect(() => {
    const request = props.document.find, view = inlineEditor();
    if (!request || !view || !interactive()) return;
    let active = true;
    onCleanup(() => { active = false; });
    queueMicrotask(() => {
      if (!active || !findController.isOpen() || !activateFindableViewForEditor(view)) return;
      setFindCaseSensitive(request.caseSensitive);
      openFind();
      view.dispatch({ selection: { anchor: Math.max(0, Math.min(view.state.doc.length, request.offset ?? 0)) } });
      setFindQuery(request.query);
    });
  });
  const gotoLine = () => {
    const editor=reader(), view=activeEditor(), content=access(); if(!view) return;
    if(!editor || !content) { openGotoLine(view); return; }
    openGotoLine({lineCount:Math.max(1,content.reference.rows),
      currentLine:(editor.reader.document.at(view.state.selection.main.head)?.slot.index ?? 0)+1,
      focus:()=>view.focus(), goTo:(line,column)=>{ void editor.revealRow(line-1,column); },
    });
  };
  const copy = () => {
    const content=access();
    if (props.document.content.kind === "retained" && !content) {
      reportCopyError(new Error("Reconnect to copy the complete document."));
      return;
    }
    void (content ? content.text(0,content.reference.total_runes,copyAbort.signal) : Promise.resolve(inlineEditor()?.state.doc.toString() ?? ""))
      .then(copyTextToClipboard).catch(reportCopyError);
  };
  createEffect(()=>{props.onHandle?.({gotoLine});onCleanup(()=>props.onHandle?.(undefined));});
  return <section ref={setHost} class="den-source-reader" data-testid="chat-content-page" aria-label={props.document.title}>
    <ReadonlyFilesDocument title={props.document.title} onFind={()=>{activeEditor()?.focus();openFind();}} onGotoLine={gotoLine} onCopy={copy}
      status={{identity:resolveEditorIdentity({historical:false,workerDraft:false,deleted:false,writable:null,preview:false,editable:false,unsupported:true}),
        windows:<DocumentWindows projectId={props.projectId} />,
        dirtyCount:projectFilesState(props.projectId).order.filter(key=>projectFilesState(props.projectId).byKey[key]?.dirty).length,
        cursor:cursor(),language:"Plain text",indent:{label:indentChipLabel(editorDisplayPrefs().indent),tip:"Editor indentation display"},
        details:<span class="den-files-editor__status-detail" data-testid="files-editor-encoding-chip">UTF-8</span>,
      }}>
    <Show when={access()} fallback={<InlineChatContent document={props.document} client={props.client} onError={reportError} onEditor={setInlineEditor} />}>
      <div class="den-source-reader__editors"><div class="den-source-reader__editor" ref={setEditorHost} /></div>
    </Show>
    </ReadonlyFilesDocument>
  </section>;
}

function InlineChatContent(props: { document: ChatContentDocument; client: LycaonClient | null; onError: (cause: unknown) => void; onEditor: (editor: EditorView | undefined) => void }) {
  const [revision,setRevision]=createSignal(0);
  const [parent,setParent]=createSignal<HTMLDivElement>();
  const handle=()=>props.document.content.kind==="process" ? props.document.content.handle : "";
  onCleanup(subscribeBackgroundProcessStore(()=>setRevision(value=>value+1),()=>({sessionId:props.document.sessionId,processId:handle()})));
  createEffect(()=>{
    const client=props.client, process=handle();
    if(client && process) void prepareBackgroundProcessOutput(client,props.document.sessionId,process).catch(props.onError);
  });
  const text=()=>{
    const content=props.document.content;
    if(content.kind==="inline") return content.text;
    if(content.kind === "retained") return (content.reference.preview_rows ?? []).map(row => row.text).join("");
    revision(); return content.kind==="process" ? getBackgroundProcessSnapshot(props.document.sessionId,content.handle)?.text ?? "" : "";
  };
  let editor: EditorView | undefined;
  const redactions = new Compartment();
  createEffect(()=>{
    const host=parent(); if(!host) return;
    const value=untrack(text), content=untrack(()=>props.document.content);
    const view=new EditorView({parent:host,state:createSourceEditorState({surface:"diff-viewer",doc:value,...untrack(editorDisplayPrefs),editable:false,scrollPastEnd:false,commandBridge:true,
      extensions:redactions.of(content.kind==="inline" ? inlineRedactions(value,content.redaction) : []),
    })});
    editor=view;
    props.onEditor(view);
    onCleanup(()=>{view.destroy(); if(editor===view) editor=undefined;props.onEditor(undefined);});
  });
  createEffect(()=>{
    const value=text(), view=editor; if(!view) return;
    const content=props.document.content;
    const effects=redactions.reconfigure(content.kind === "inline" ? inlineRedactions(value,content.redaction) : []);
    const previous=view.state.doc.toString();
    if(previous===value) { view.dispatch({effects}); return; }
    let from=0,to=previous.length,end=value.length;
    while(from<to && from<end && previous[from]===value[from]) from++;
    while(to>from && end>from && previous[to-1]===value[end-1]) {to--;end--;}
    view.dispatch({changes:{from,to,insert:value.slice(from,end)},effects});
  });
  createEffect(()=>{const prefs=editorDisplayPrefs();if(editor) applyEditorDisplayPrefs(editor,prefs);});
  return <div class="den-source-reader__editors"><div class="den-source-reader__editor" ref={setParent} /></div>;
}

function inlineRedactions(text: string, redaction?: HostSecretRedactionMeta) {
  const offsets=[0];for(const rune of text) offsets.push((offsets.at(-1) ?? 0)+rune.length);
  const marks=(redaction?.spans ?? []).filter(span=>span.field==="tool_result.content").flatMap(span=>{
    const from=offsets[span.start],to=offsets[span.start+span.length];
    return from!==undefined && to!==undefined && to>from ? [Decoration.mark({class:["den-redaction-mark",redactionMarkModifier(span)].filter(Boolean).join(" "),attributes:{"data-tip":redactionMarkTitle(span)}}).range(from,to)] : [];
  });
  return EditorView.decorations.of(Decoration.set(marks,true));
}
