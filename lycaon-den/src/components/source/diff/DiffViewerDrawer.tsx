import { For, Show, createMemo, createSignal, onMount, onCleanup } from "solid-js";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import { DenOverlay } from "../../overlay/DenOverlay.tsx";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "../../ContextMenu.tsx";
import { SourceReader } from "../reader/SourceReader.tsx";
import { SourceChangeChip } from "./SourceChangeChip.tsx";
import { readerComparesSides, wholeFileChange } from "../reader/source-reader-change.ts";
import { closeDiffViewer, type DiffViewerRequest } from "../../../platform/navigation/in-app-diff.ts";
import { copyTextToClipboard } from "../../../utils/clipboard.ts";
import { addToChat } from "../../../chat/composer/add-to-chat.ts";
import { startChatAttachmentDrag } from "../../../chat/composer/chat-attachment-drag.ts";
import type { ResolveProjectRoot } from "../../../api/project-path.ts";
import { relativeUnderRoot } from "../../../api/project-path.ts";
import { openSourceLocation } from "../../../platform/navigation/open-source.ts";
import { pathMenuItems } from "../../path-menu-items.ts";
import { copyPathMenuItems } from "../../copy-path-menu-items.ts";
import {
  bindChromeContextMenu,
  overflowMenuAnchor,
} from "../../context-menu-open.ts";
import { languageLabelForPath } from "../editor/codemirror-lang.ts";
import { CheckIcon, CopyIcon, MoreIcon } from "../viewer-icons.tsx";
import {
  diffSplitPref,
  diffWordWrapPref,
  saveDiffSplit,
} from "../../../settings/appearance/display-prefs.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";

type Props = {
  request: DiffViewerRequest;
  rootRefs: readonly ResolveProjectRoot[];
};

function splitPathSegments(path: string): { dirs: string[]; file: string } {
  const parts = path.replace(/\\/g, "/").split("/").filter(Boolean);
  const file = parts.pop() ?? path;
  return { dirs: parts, file };
}

export function DiffViewerDrawer(props: Props) {
  const request = createMemo(() => props.request);
  const [copiedPath, setCopiedPath] = createSignal(false);
  const [toolbarMenu, setToolbarMenu] = createSignal<{
    kind: "copy" | "more";
    anchor: ContextMenuAnchor;
    align?: "start" | "end";
  } | null>(null);

  let moreBtnEl: HTMLButtonElement | undefined;
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;

  const dismiss = () => closeDiffViewer();
  const path = () => request().path;
  const absolutePath = (): string | undefined => request().absolutePath;
  const projectRoots = () => props.rootRefs.map((root) => root.path);

  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (toolbarMenu() != null) return;
      e.preventDefault();
      dismiss();
    };
    window.addEventListener("keydown", onKey);
    onCleanup(() => {
      window.removeEventListener("keydown", onKey);
      if (copiedTimer) clearTimeout(copiedTimer);
    });
  });

  const comparesSides = () => readerComparesSides(request().change);
  const split = () => diffSplitPref() && comparesSides();

  const flashCopied = () => {
    setCopiedPath(true);
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => setCopiedPath(false), 1400);
  };
  const onCopy = () => {
    const abs = absolutePath();
    void copyTextToClipboard(abs ?? path());
    flashCopied();
  };
  const onCopyRelative = () => {
    void copyTextToClipboard(path());
    flashCopied();
  };
  const onCopyFileName = () => {
    void copyTextToClipboard(splitPathSegments(path()).file);
    flashCopied();
  };
  const addToChatRef = createMemo(() => {
    const projectId = request().projectId?.trim() ?? "";
    const rootId = request().rootId?.trim() ?? "";
    const abs = absolutePath();
    if (!projectId || !rootId || !abs) return null;
    const root = props.rootRefs.find((candidate) => candidate.id === rootId);
    if (!root) return null;
    const relativePath = relativeUnderRoot(abs, root.path);
    if (relativePath == null || relativePath === ".") return null;
    return {
      kind: "path-file" as const,
      projectId,
      rootId: root.id,
      path: relativePath,
      name: splitPathSegments(relativePath).file,
    };
  });
  const onAddToChat = () => {
    const ref = addToChatRef();
    if (ref) void addToChat(ref);
  };

  const openToolbarMenu = (
    kind: "copy" | "more",
    anchor: ContextMenuAnchor,
    align?: "start" | "end",
  ) => setToolbarMenu({ kind, anchor, align });
  const copyMenuBind = bindChromeContextMenu((anchor) =>
    openToolbarMenu("copy", anchor),
  );
  const moreMenuBind = bindChromeContextMenu((anchor) =>
    openToolbarMenu("more", anchor),
  );

  const openMoreMenu = () => {
    const anchor = overflowMenuAnchor(moreBtnEl);
    if (!anchor) return;
    openToolbarMenu("more", anchor, "end");
  };

  const copyMenuItems = (): ContextMenuItem[] =>
    copyPathMenuItems({
      copyAbsolute: absolutePath() ? onCopy : null,
      copyRelative: onCopyRelative,
      copyFileName: onCopyFileName,
      absoluteTestId: "diff-viewer-copy-path",
      relativeTestId: "diff-viewer-copy-relative-path",
      fileNameTestId: "diff-viewer-copy-file-name",
    });

  const moreMenuItems = (): ContextMenuItem[] => {
    const items: ContextMenuItem[] = [];
    const abs = absolutePath();
    if (abs) {
      const ref = addToChatRef();
      items.push(...pathMenuItems({
        absoluteTestId: "diff-viewer-copy-path", relativeTestId: "diff-viewer-copy-relative-path",
        revealInTree: ref ? {
          testId: "diff-viewer-reveal-tree",
          onSelect: () => {
            void openSourceLocation({ projectId: ref.projectId, rootId: ref.rootId, path: ref.path, intent: "permanent", action: "reveal" })
              .then((result) => { if (result.status === "opened-in-app") closeDiffViewer(); });
          },
        } : undefined,
        openIn: { absolutePath: abs, projectRoots: projectRoots(), entryKind: "file" },
        addToChat: ref ? { onSelect: onAddToChat, testId: "diff-viewer-add-to-chat" } : undefined,
      }));
    }
    return items;
  };

  const pathParts = () => splitPathSegments(path());

  return (
    <ResidentPortal mount={document.body}>
      <DenOverlay
        data-testid="diff-viewer-overlay"
        onClick={(e) => {
          if (e.target === e.currentTarget) dismiss();
        }}
      >
        <div
          class="den-overlay__panel den-diff-viewer"
          role="dialog"
          aria-modal="true"
          aria-label={`Diff for ${path()}`}
          data-testid="diff-viewer"
          data-project-id={(request().projectId ?? "").trim() || undefined}
          data-root-id={(request().rootId ?? "").trim() || undefined}
          data-den-source-path={path().trim() || undefined}
          onClick={(e) => e.stopPropagation()}
        >
          <header class="den-overlay__header den-diff-viewer__header" {...chromeProps()}>
            <div class="den-overlay__header-main den-diff-viewer__path">
              <span class="den-diff-viewer__crumbs" aria-hidden="true">
                <For each={pathParts().dirs}>
                  {(seg) => (
                    <>
                      <span class="den-diff-viewer__crumb">{seg}</span>
                      <span class="den-diff-viewer__crumb-sep">/</span>
                    </>
                  )}
                </For>
              </span>
              <span
                class="den-diff-viewer__path-file"
                data-testid="diff-viewer-path"
                data-file-change={wholeFileChange(request().change)}
                draggable={addToChatRef() ? true : undefined}
                onDragStart={(event) =>
                  startChatAttachmentDrag(event, addToChatRef())
                }
              >
                {pathParts().file}
              </span>
              <SourceChangeChip change={request().change} />
              <span
                class="den-diff-viewer__lang den-status-mark"
                data-testid="diff-viewer-lang"
              >
                {languageLabelForPath(path())}
              </span>
            </div>
            <div class="den-diff-viewer__actions">
              <BrowseSegmented
                testId="diff-viewer-layout"
                ariaLabel="Diff layout"
                value={split() ? "split" : "unified"}
                disabled={!comparesSides()}
                onChange={(id) => {
                  if (id === "split" || id === "unified") {
                    void saveDiffSplit(id === "split");
                  }
                }}
                options={[
                  { id: "unified", label: "Unified", testId: "diff-viewer-unified" },
                  { id: "split", label: "Split", testId: "diff-viewer-split" },
                ]}
              />
              <button
                type="button"
                class="den-diff-viewer__tool den-inset-icon-btn"
                classList={{
                  "den-diff-viewer__tool--ok": copiedPath(),
                }}
                data-testid="diff-viewer-copy"
                data-tip="Copy path"
                aria-label="Copy path"
                aria-haspopup="menu"
                aria-expanded={toolbarMenu()?.kind === "copy"}
                onClick={onCopy}
                onContextMenu={copyMenuBind.onContextMenu}
                onKeyDown={copyMenuBind.onKeyDown}
              >
                <Show when={copiedPath()} fallback={<CopyIcon />}>
                  <CheckIcon />
                </Show>
              </button>
              <Show when={moreMenuItems().length > 0}>
                <button
                  ref={moreBtnEl}
                  type="button"
                  class="den-diff-viewer__tool den-inset-icon-btn"
                  data-testid="diff-viewer-more"
                  aria-label="More actions"
                  aria-haspopup="menu"
                  aria-expanded={toolbarMenu()?.kind === "more"}
                  onClick={openMoreMenu}
                  onContextMenu={moreMenuBind.onContextMenu}
                  onKeyDown={moreMenuBind.onKeyDown}
                >
                  <MoreIcon />
                </button>
              </Show>
              <span class="den-diff-viewer__actions-sep" aria-hidden="true" />
              <button
                type="button"
                class="den-overlay__close den-inset-icon-btn"
                data-testid="diff-viewer-close"
                aria-label="Close"
                onClick={dismiss}
              >
                ×
              </button>
            </div>
          </header>

          <div
            class="den-overlay__body den-diff-viewer__diff-body"
            data-testid="diff-viewer-body"
          >
            <SourceReader projectId={request().projectId?.trim()} access={request().reader} path={path()} wrap={diffWordWrapPref()} split={split()} primaryFind />
          </div>
        </div>
      </DenOverlay>
      <Show when={toolbarMenu()} keyed>
        {(menu) => (
          <ContextMenu
            anchor={menu.anchor}
            align={menu.align}
            items={menu.kind === "copy" ? copyMenuItems() : moreMenuItems()}
            onDismiss={() => setToolbarMenu(null)}
          />
        )}
      </Show>
    </ResidentPortal>
  );
}
