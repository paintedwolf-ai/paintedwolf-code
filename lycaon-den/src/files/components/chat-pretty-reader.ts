import { Text } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { createEffect, createMemo, createSignal, onCleanup, untrack, type Accessor } from "solid-js";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import type { ChatContentAccess, ChatContentBoundedSnapshot } from "../../chat/transcript/content/chat-content-reader.ts";
import { CommandReceiptLimitError } from "../../chat/transcript/content/command-receipt-access.ts";
import { planHasLayout } from "../../components/source/pretty/pretty-fenced.ts";
import { prettyPlanFor, prettyTargetFor } from "../../components/source/pretty/pretty-recognition.ts";
import { restoreScrollAnchor } from "../../components/source/editor/editor-settled-scroll.ts";
import type { ChatContentEditor } from "../editor/chat-content-editor.ts";
import { inlineRange, inlineReadingPlace, type ChatReadingPlace } from "../editor/chat-content-viewport.ts";
import { redactionSpansFit } from "./chat-content-pretty.ts";

type Handoff = { access: ChatContentAccess; revision: string; place: ChatReadingPlace };

/** Keeps each reader's handoff with the revision that supplied its text. */
export function useChatPrettyReader(
  document: Accessor<ChatContentDocument>, access: Accessor<ChatContentAccess | undefined>,
  enabled: Accessor<boolean>, reader: Accessor<ChatContentEditor | undefined>,
  inline: Accessor<EditorView | undefined>, onChanging: (cause: unknown) => void,
) {
  const [snapshot, setSnapshot] = createSignal<ChatContentBoundedSnapshot>();
  const path = createMemo(() => document().path);
  const language = createMemo(() => document().language);
  const target = createMemo(() => prettyTargetFor(path(), language()));
  let shownAccess: ChatContentAccess | undefined;
  let handoff: Handoff | undefined;
  const takePlace = (source: ChatContentAccess) => {
    const next = handoff;
    handoff = undefined;
    return next?.access === source && next.revision === source.revision() ? next.place : undefined;
  };
  createEffect(() => {
    const source = access(), layout = target(), pretty = enabled();
    const revision = source?.revision();
    const current = untrack(snapshot);
    if (current && source && current.revision === revision && source === shownAccess) {
      const view = untrack(inline);
      const place = view && inlineReadingPlace(view, untrack(document).content.kind === "command_receipts");
      handoff = place && { access: source, revision, place };
    } else {
      handoff = undefined;
    }
    shownAccess = undefined;
    setSnapshot(undefined);
    if (!source || !pretty || !layout) return;
    const abort = new AbortController();
    onCleanup(() => abort.abort());
    void source.prettySnapshot(abort.signal).then(async result => {
      if (abort.signal.aborted || !result || "limit" in result || result.revision !== revision) return;
      const doc = Text.of(result.text.split("\n"));
      if (!planHasLayout(doc, prettyPlanFor(doc, layout)) || !redactionSpansFit(result.text, result.spans)) return;
      const place = await reader()?.capturePlace(abort.signal);
      if (abort.signal.aborted || source.revision() !== revision) return;
      handoff = place && { access: source, revision, place };
      shownAccess = source;
      setSnapshot(result);
    }).catch(cause => {
      if (!abort.signal.aborted && cause instanceof CommandReceiptLimitError) onChanging(cause);
    });
  });
  const restoreInline = (view: EditorView) => {
    const source = access();
    const place = source && takePlace(source);
    if (!place) return;
    const { anchor } = inlineRange(view.state.doc, place.target);
    return restoreScrollAnchor(view, { pos: anchor, offset: place.offset, left: place.left });
  };
  return { snapshot, takePlace, restoreInline };
}
