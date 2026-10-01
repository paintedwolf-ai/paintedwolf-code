import { For, Show, createEffect, createSignal, onCleanup, untrack } from "solid-js";
import type { Token } from "marked";
import {
  MARKDOWN_PREVIEW_ASYNC_CHARS,
  type MarkdownPreviewResponse,
} from "../../chat/markdown/markdown-preview-document.ts";
import { perfMark } from "../../chat/stream/den-main-thread-perf.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { createPresentationIntent, createPresentationWaiting } from "../../ui/presentation.ts";
import { afterPaint } from "../../ui/surface-reveal.ts";
import { MarkdownBody } from "../../components/transcript/MarkdownBody.tsx";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";

import { VirtualMarkdownDocument, type PreviewDocument } from "./VirtualMarkdownDocument.tsx";

type PreviewProps = {
  source: string;
  projectId: string;
  preparing?: boolean;
  waitingVisible?: boolean;
  onReadyChange?: (ready: boolean) => void;
};

export function FileMarkdownPreview(props: PreviewProps) {
  let scrollport!: HTMLDivElement;
  createEffect(() => {
    if (props.source.length <= MARKDOWN_PREVIEW_ASYNC_CHARS) props.onReadyChange?.(true);
  });
  return (
    <Scrollport class="den-files-editor__preview"
      classList={{ "den-files-editor__preview--preparing": props.preparing }}
      contentClass="den-files-editor__preview-content"
      viewportRef={(el) => { scrollport = el; }}
      aria-hidden={props.preparing ? true : undefined}
      inert={props.preparing ? true : undefined}
      data-testid="files-editor-preview">
      <Show when={props.source.length > MARKDOWN_PREVIEW_ASYNC_CHARS} fallback={
        <MarkdownBody source={props.source} projectId={props.projectId} class="den-files-editor__preview-body" />
      }>
        <LargeMarkdownPreview {...props} scrollport={() => scrollport} />
      </Show>
    </Scrollport>
  );
}

function LargeMarkdownPreview(props: PreviewProps & { scrollport: () => HTMLDivElement }) {
  const live = useResidentLive();
  const intent = createPresentationIntent();
  const [displayed, setDisplayed] = createSignal<PreviewDocument>();
  const [documents, setDocuments] = createSignal<PreviewDocument[]>([]);
  const [pending, setPending] = createSignal(true);
  const [failed, setFailed] = createSignal<string | null>(null);
  const waiting = createPresentationWaiting(pending);
  onCleanup(intent.dispose);
  observeSurfaceFailure(
    { code: "files_markdown_preview_unavailable", title: "Could not prepare the preview", suggestedAction: "Switch to the source view, or edit the file to prepare the preview again." },
    failed,
    () => props.projectId,
  );

  createEffect(() => {
    const source = props.source;
    if (!live()) return;
    const attempt = intent.begin();
    setPending(true);
    setFailed(null);
    props.onReadyChange?.(false);
    setDocuments((current) => current.filter((item) => item === untrack(displayed)));
    let worker: Worker | undefined;
    const readers = new Map<number, (tokens: Token[]) => void>();
    const fail = () => attempt.commit(() => {
      intent.cancel();
      setDocuments([]);
      setDisplayed(undefined);
      setFailed("The Markdown preview could not be prepared.");
      setPending(false);
      props.onReadyChange?.(true);
      worker?.terminate();
      readers.clear();
    });
    const cancelPaint = afterPaint(() => {
      try {
        perfMark("markdown.preview.prepare", { chars: source.length });
        worker = new Worker(new URL("../../chat/markdown/markdown-preview.worker.ts", import.meta.url), { type: "module" });
        worker.onerror = fail;
        worker.onmessageerror = fail;
        worker.onmessage = (event: MessageEvent<MarkdownPreviewResponse>) => {
          if (!attempt.current()) return;
          const message = event.data;
          if (message.type === "error") fail();
          else if (message.type === "block") readers.get(message.index)?.(message.tokens);
          else {
            perfMark("markdown.preview.ready", { blocks: message.blocks.length });
            const candidate: PreviewDocument = {
              blocks: message.blocks,
              read(index, receive) {
                readers.set(index, receive);
                worker?.postMessage({ type: "block", index });
                return () => {
                  if (readers.get(index) === receive) readers.delete(index);
                };
              },
              ready: () => attempt.commit(() => {
                setDisplayed(candidate);
                setDocuments([candidate]);
                setPending(false);
                props.onReadyChange?.(true);
              }),
            };
            setDocuments((current) => [...current, candidate]);
          }
        };
        worker.postMessage({ type: "prepare", source });
      } catch {
        fail();
      }
    });
    onCleanup(() => {
      intent.cancel();
      cancelPaint();
      worker?.terminate();
      readers.clear();
    });
  });

  return (
    <div class="den-markdown-preview-presentation" aria-busy={pending()}>
      <For each={documents()}>{(document) => (
        <div
          class="den-files-editor__preview-body den-retained-presentation"
          classList={{ "den-markdown-preview-candidate": displayed() !== document }}
          data-retained={displayed() === document && waiting() ? "true" : "false"}
          aria-hidden={pending() ? true : undefined}
          inert={pending() ? true : undefined}
        >
          <VirtualMarkdownDocument document={document} projectId={props.projectId} scrollport={props.scrollport} />
        </div>
      )}</For>
      <Show when={waiting() && props.waitingVisible !== false}>
        <div class="den-presentation-wait" role="status">Preparing preview…</div>
      </Show>
    </div>
  );
}

