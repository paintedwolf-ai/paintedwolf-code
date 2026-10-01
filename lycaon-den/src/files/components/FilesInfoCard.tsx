import { captureReaderBufferViewState, readerBufferViewState } from "../editor/editor-session-fidelity.ts";
import type { LycaonClient } from "../../api/client.ts";
import { sourceReaderAccess } from "../../api/source-reader.ts";
import { SourceReader, type SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { explicitSourceEncoding } from "../source/files-source-read.ts";
import { contextAction } from "../../components/context-actions.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { Show, createEffect, createSignal, createMemo, onCleanup } from "solid-js";
import type { ProjectRoot } from "../../api/types.ts";
import { OpenInButton } from "../../components/OpenInButton.tsx";
import { fileOpenTarget } from "../source/file-open-target.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { copyPathMenuItems } from "../../components/copy-path-menu-items.ts";
import { bindChromeContextMenu } from "../../components/context-menu-open.ts";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "../../components/ContextMenu.tsx";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import {
  CheckIcon,
  CopyIcon,
} from "../../components/source/viewer-icons.tsx";
import {
  formatSourceBytes,
  formatSourceMimeLabel,
  formatSourceMtime,
} from "../../components/source/editor/source-editor-model.ts";
import {
  infoCardExplanation,
  infoCardTitle,
  infoCardVariant,
} from "../documents/project-files-buffer-kind.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { absolutePathForBuffer } from "./project-files-model.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { FilesPathCrumbs } from "../editor/FilesPathCrumbs.tsx";

type Props = {
  projectId?: string;
  client?: LycaonClient | null;
  sessionId?: string;
  onReaderHandle?: (handle: SourceReaderHandle | undefined) => void;
  buffer: FileBuffer;
  roots: ProjectRoot[];
  onInnerLayerChange: (open: boolean) => void;
  onReopenAsUTF16?: (encoding: "utf-16le" | "utf-16be") => void;
  onReload?: () => void;
  onRevealSegment: (rootId: string, path: string, isDir: boolean) => void;
};

function FileTypeIcon() {
  return <ThemeIcon slot="file" size={48} class="den-files-info__icon" />;
}

export function FilesInfoCard(props: Props) {
  const [readerHandle, setReaderHandle] = createSignal<SourceReaderHandle>();
  const [readyReader, setReadyReader] = createSignal<ReturnType<typeof sourceReaderAccess>>();
  const reader = createMemo(() => {
    const client = props.client, projectId = props.projectId, buffer = props.buffer;
    if (!client || !projectId || (buffer.content.state === "suspended" || buffer.content.state === "unloaded") || buffer.loading || buffer.loadError || !buffer.overLimit || !buffer.encoding || buffer.jobId) return undefined;
    void buffer.mtime;
    return sourceReaderAccess(client, projectId, { kind: "current", root_id: buffer.rootId, path: buffer.path, decode_as: explicitSourceEncoding(buffer.encoding) }, props.sessionId);
  });
  usePresentationParticipant("file-reader", () => !reader() || reader() === readyReader());
  createEffect(() => {
    const access = reader();
    onCleanup(() => access?.release?.());
  });
  observeSurfaceFailure(
    { code: "files_document_unavailable", title: "Could not load the file", suggestedAction: "Reload the file, or reopen it from the file tree." },
    () => (props.projectId ? props.buffer.loadError : null),
    () => props.projectId ?? "",
  );
  const [copiedPath, setCopiedPath] = createSignal(false);
  const [openInMenuOpen, setOpenInMenuOpen] = createSignal(false);
  const [toolbarMenu, setToolbarMenu] = createSignal<{
    anchor: ContextMenuAnchor;
    kind: "copy" | "encoding";
  } | null>(null);

  createEffect(() => {
    props.onInnerLayerChange(openInMenuOpen() || toolbarMenu() != null);
  });

  const root = () => props.roots.find((r) => r.id === props.buffer.rootId);
  const absolutePath = (): string | undefined => {
    const r = root();
    return r ? absolutePathForBuffer(r.path, props.buffer.path) : undefined;
  };
  const variant = () =>
    infoCardVariant({
      overLimit: props.buffer.overLimit,
      unsupportedEncodingDetected: props.buffer.unsupportedEncodingDetected,
    });
  const title = () => infoCardTitle(variant());
  const explanation = () =>
    infoCardExplanation(variant(), props.buffer.unsupportedEncodingDetected);

  let copiedTimer: ReturnType<typeof setTimeout> | undefined;
  onCleanup(() => {
    if (copiedTimer) clearTimeout(copiedTimer);
  });

  const flashCopied = () => {
    setCopiedPath(true);
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => setCopiedPath(false), 1400);
  };

  const onCopyPath = () => {
    const abs = absolutePath();
    if (!abs) return;
    void copyTextToClipboard(abs);
    flashCopied();
  };

  const onCopyRelativePath = () => {
    void copyTextToClipboard(props.buffer.path);
    flashCopied();
  };

  const onCopyFileName = () => {
    void copyTextToClipboard(props.buffer.name);
    flashCopied();
  };

  const copyMenuItems = (): ContextMenuItem[] =>
    copyPathMenuItems({
      copyAbsolute: absolutePath() ? onCopyPath : null,
      copyRelative: onCopyRelativePath,
      copyFileName: onCopyFileName,
      absoluteTestId: "files-info-copy-path",
      relativeTestId: "files-info-copy-relative-path",
      fileNameTestId: "files-info-copy-file-name",
    });

  const copyMenuBind = bindChromeContextMenu((anchor) =>
    setToolbarMenu({ anchor, kind: "copy" }),
  );

  return (
    <div class="den-files-info" data-testid="files-info-card" data-files-ctx="info-card">
      <div class="den-files-toolbar">
        <FilesPathCrumbs
          rootId={props.buffer.rootId}
          rootLabel={props.buffer.rootLabel}
          path={props.buffer.path}
          onRevealSegment={props.onRevealSegment}
        />
        <span class="den-files-editor__toolbar-actions">
          <button
            type="button"
            class="den-files-editor__tool den-inset-icon-btn"
            classList={{ "den-files-editor__tool--ok": copiedPath() }}
            data-testid="files-info-copy-toolbar"
            data-tip="Copy path"
            data-tip-pos="below"
            aria-label="Copy path"
            aria-haspopup="menu"
            aria-expanded={toolbarMenu()?.kind === "copy"}
            onClick={onCopyPath}
            onContextMenu={copyMenuBind.onContextMenu}
            onKeyDown={copyMenuBind.onKeyDown}
          >
            <Show when={copiedPath()} fallback={<CopyIcon />}>
              <CheckIcon />
            </Show>
          </button>
        </span>
      </div>
      <Show when={props.buffer.loading}>
        <p class="den-files-editor__notice" data-testid="files-info-loading">
          Loading…
        </p>
      </Show>
      <Show when={!props.buffer.loading && !props.buffer.loadError}>
        <Show keyed when={reader()} fallback={
        <Scrollport class="den-files-info__body" contentClass="den-files-info__center">
            <div class="den-files-info__card">
              <FileTypeIcon />
              <h2 class="den-files-info__name" data-testid="files-info-name">
                {props.buffer.name}
              </h2>
              <Show when={title()}>
                <p class="den-files-info__title" data-testid="files-info-title">
                  {title()}
                </p>
              </Show>
              <Show when={variant() !== "unsupported_encoding"}>
                <dl class="den-files-info__meta">
                  <div class="den-files-info__meta-row">
                    <dt>Size</dt>
                    <dd data-testid="files-info-size">{formatSourceBytes(props.buffer.sizeBytes)}</dd>
                  </div>
                  <div class="den-files-info__meta-row">
                    <dt>Modified</dt>
                    <dd data-testid="files-info-mtime">
                      {formatSourceMtime(props.buffer.mtime)}
                    </dd>
                  </div>
                  <div class="den-files-info__meta-row">
                    <dt>Type</dt>
                    <dd data-testid="files-info-type">
                      {formatSourceMimeLabel(props.buffer.mime)}
                    </dd>
                  </div>
                </dl>
              </Show>
              <p class="den-files-info__explain" data-testid="files-info-explain">
                {explanation()}
              </p>
              <div class="den-files-info__actions">
                <Show when={variant() === "unsupported_encoding"}>
                  <DenButton
                    variant="primary"
                    data-testid="files-info-open-utf16le-btn"
                    onClick={() => props.onReopenAsUTF16?.("utf-16le")}
                  >
                    Open as UTF-16 LE
                  </DenButton>
                  <DenButton
                    variant="ghost"
                    data-testid="files-info-open-utf16be-btn"
                    onClick={() => props.onReopenAsUTF16?.("utf-16be")}
                  >
                    Open as UTF-16 BE
                  </DenButton>
                </Show>
                <OpenInButton target={fileOpenTarget(props.buffer, props.roots)} onOpenChange={setOpenInMenuOpen} />
                <Show when={variant() === "binary" && props.onReopenAsUTF16}>
                  <DenButton
                    variant="ghost"
                    data-testid="files-info-open-text-btn"
                    aria-haspopup="menu"
                    aria-expanded={toolbarMenu()?.kind === "encoding"}
                    onClick={(event) => {
                      const rect = event.currentTarget.getBoundingClientRect();
                      setToolbarMenu({ anchor: { x: rect.left, y: rect.bottom }, kind: "encoding" });
                    }}
                  >
                    Open as text…
                  </DenButton>
                </Show>
                <Show when={variant() !== "unsupported_encoding"}>
                  <DenButton
                    variant="ghost"
                    data-testid="files-info-copy-path-btn"
                    disabled={!absolutePath()}
                    onClick={onCopyPath}
                  >
                    Copy path
                  </DenButton>
                  <DenButton
                    variant="ghost"
                    data-testid="files-info-copy-relative-path-btn"
                    onClick={onCopyRelativePath}
                  >
                    Copy relative path
                  </DenButton>
                </Show>
              </div>
            </div>
        </Scrollport>
        }>{access => <SourceReader projectId={props.projectId} access={access} path={props.buffer.path} current primaryFind scrollPastEnd
          onReady={ready => setReadyReader(ready ? access : undefined)} onHandle={handle => { setReaderHandle(handle); props.onReaderHandle?.(handle); }}
          restoreViewState={sha => readerBufferViewState(props.buffer, sha)}
          onViewState={(sha, state) => captureReaderBufferViewState(props.buffer, sha, state)}
          leadingActions={<span>Read-only · {formatSourceBytes(props.buffer.sizeBytes)}</span>}
          trailingActions={<><DenButton variant="ghost" compact onClick={() => { void readerHandle()?.gotoLine(); }}>Go to line</DenButton>
            <DenButton variant="ghost" compact onClick={() => props.onReload?.()}>Reload</DenButton></>} />}</Show>
      </Show>
      <Show when={toolbarMenu()} keyed>
        {(menu) => (
          <ContextMenu
            anchor={menu.anchor}
            items={menu.kind === "copy" ? copyMenuItems() : [
              contextAction("openAsUTF16LE", {  testId: "files-info-open-utf16le-menu", onSelect: () => props.onReopenAsUTF16?.("utf-16le") }),
              contextAction("openAsUTF16BE", {  testId: "files-info-open-utf16be-menu", onSelect: () => props.onReopenAsUTF16?.("utf-16be") }),
            ]}
            onDismiss={() => setToolbarMenu(null)}
          />
        )}
      </Show>
    </div>
  );
}
