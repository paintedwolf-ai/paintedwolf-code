import { SurfaceDeck } from "../../components/primitives/SurfaceDeck.tsx";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import type { SourceContributor } from "../../api/types.ts";
import { Show, createMemo, createEffect, createSignal } from "solid-js";
import type { DenEditorVersionComparison } from "../../../shared/app-state-types.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ContextMenuAnchor } from "../../components/ContextMenu.tsx";
import { SourceReader, type SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";
import { sourceReaderAccess, currentComparisonAccess } from "../../api/source-reader.ts";
import { fileVersionIsReadable, versionAfterSide, versionBeforeSide, type FileVersionView } from "./file-version.ts";
import { currentDisplayPrefs } from "../editor/files-editor-display.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { sourceContentNotice, type ContentNotice } from "../source/source-content-availability.ts";

type FileVersionDocumentProps = {
  buffer: FileBuffer;
  client: LycaonClient | null | undefined;
  projectId: string;
  sessionId?: string;
  version: () => FileVersionView | null;
  preview?: boolean;
  comparison: () => DenEditorVersionComparison;
  currentContent: () => string | null;
  comparisonMenuOpen: () => boolean;
  onOpenComparisonMenu: (anchor: ContextMenuAnchor) => void;
  onCurrent: () => void;
  onContributor?: (author: SourceContributor, line: number) => void;
  onReady?: (ready: boolean) => void;
  onHandle?: (handle: SourceReaderHandle | undefined) => void;
};

export function FileVersionDocument(props: FileVersionDocumentProps) {
  let sequence = 0;
  const candidate = createMemo(() => ({
    key: String(++sequence),
    version: props.version(),
    comparison: props.comparison(),
    client: props.client,
    projectId: props.projectId,
    sessionId: props.sessionId,
    preview: props.preview,
    identity: JSON.stringify([props.version()?.source, props.version()?.rootId, props.version()?.path, props.version()?.availability, props.version()?.beforeAvailability]),
    content: props.version()?.source.kind === "text" && props.comparison() === "current" ? props.currentContent() : null,
  }), undefined, { equals: (previous, next) => previous.client === next.client && previous.projectId === next.projectId &&
    previous.sessionId === next.sessionId && previous.preview === next.preview && previous.comparison === next.comparison &&
    previous.content === next.content && previous.identity === next.identity });
  const [displayed, setDisplayed] = createSignal<string | null>(null);
  createEffect(() => { void props.version(); props.onReady?.(displayed() === candidate().key); });
  return <SurfaceDeck class="den-files-tab-deck" active={candidate().key} retain={key => key === candidate().key} retainOutgoing
    onDisplayedChange={setDisplayed}>
    {() => {
      const selected = candidate();
      const [ready, setReady] = createSignal(false);
      usePresentationParticipant("historical-document", ready);
      return <FileVersionContent {...props} client={selected.client} projectId={selected.projectId} sessionId={selected.sessionId} preview={selected.preview}
        version={() => selected.version} comparison={() => selected.comparison} currentContent={() => selected.content}
        onReady={setReady} onHandle={handle => { if (selected === candidate()) props.onHandle?.(handle); }} />;
    }}
  </SurfaceDeck>;
}

function FileVersionContent(props: FileVersionDocumentProps) {
  const access = createMemo(() => {
    const version = props.version(); const client = props.client;
    if (!version || !client || !fileVersionIsReadable(version)) return undefined;
    if (version.source.kind === "reader") {
      if (props.comparison() === "before" || props.preview) return sourceReaderAccess(client, props.projectId, version.source.reader, props.sessionId);
      const original = sourceReaderAccess(client, props.projectId, version.source.reader, props.sessionId);
      return currentComparisonAccess(original, client, props.projectId, props.buffer.rootId, props.buffer.path, props.sessionId);
    }
    if (version.source.kind !== "text") return undefined;
    return sourceReaderAccess(client, props.projectId, { kind: "text", path: version.path, before: props.comparison() === "current" ? props.currentContent() : version.beforeAvailability === "absent" ? null : version.source.before, after: version.availability === "absent" ? null : version.source.after }, props.sessionId);
  });
  createEffect(() => { if (!access()) props.onReady?.(true); });
  // The shown text always needs the after side; comparing with before needs both.
  const unavailable = (): ContentNotice | null => {
    if (!props.client) return { tone: "error", message: "Reconnect to read this comparison." };
    const version = props.version();
    if (!version) return null;
    return props.buffer.diffPreview?.notice
      ?? sourceContentNotice(versionAfterSide(version))
      ?? (props.comparison() === "before" ? sourceContentNotice(versionBeforeSide(version)) : null);
  };
  return <div class="den-file-version__document" data-testid="file-version-document" hidden={!props.version()}>
    <Show when={unavailable()}>{notice => <div class="den-file-version__state" classList={{ "den-file-version__state--error": notice().tone === "error" }} role={notice().tone === "error" ? "alert" : "status"}>{notice().message}</div>}</Show>
    <Show when={!props.preview && !access()}><div class="den-document-actions"><button type="button" class="den-document-action den-document-action--exit" data-testid="file-version-exit" aria-label="Back to the current file" data-tip={`Back to the current file (${bindingForHandler("files.currentVersion")} or Esc)`} data-tip-pos="below" onClick={props.onCurrent}><ThemeIcon slot="back" size={11} />Current</button></div></Show>
    <div class="den-file-version__host" data-testid="file-version-file">
      <Show keyed when={access()}>{reader => <SourceReader projectId={props.projectId} access={reader} path={props.version()?.path ?? props.buffer.path}
        leadingActions={<Show when={!props.preview}><button type="button" class="den-document-action den-document-action--exit" data-testid="file-version-exit" aria-label="Back to the current file" data-tip={`Back to the current file (${bindingForHandler("files.currentVersion")} or Esc)`} data-tip-pos="below" onClick={props.onCurrent}><ThemeIcon slot="back" size={11} />Current</button></Show>}
        trailingActions={<Show when={!props.preview}><button type="button" class="den-document-action" data-testid="file-version-compare-trigger" aria-haspopup="menu" aria-expanded={props.comparisonMenuOpen()} data-tip={`Compare options (${bindingForHandler("files.toggleComparison")})`} data-tip-pos="below" onClick={event => props.onOpenComparisonMenu(event.currentTarget)}>Compare: {props.comparison() === "current" ? "Current" : "Before"} ▾</button></Show>}
        displayPrefs={currentDisplayPrefs(props.buffer)} primaryFind onContributor={props.onContributor} onReady={props.onReady} onHandle={props.onHandle} />}</Show>
    </div>
  </div>;
}
