import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import type { DocumentReplica } from "../documents/document-replica.ts";
import { closeItemWindow, itemWindowViews } from "../../platform/windows/item-windows.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { editorWindow, focusEditorWindow, type EditorWindow } from "../../platform/windows/editor-windows.ts";
import { readWindowPalette, windowColorStyle } from "../../contributions/window-color-palette.ts";
import { watchPaletteTheme } from "../../contributions/palette-cache.ts";
import { AnchoredSurface } from "../../components/primitives/AnchoredSurface.tsx";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";

const WINDOW_UNAVAILABLE = {
  code: "document_window_unavailable",
  title: "Window unavailable",
  suggestedAction: "Pick another window from the list.",
};

export function DocumentWindows(props: { projectId: string; replica?: DocumentReplica; peerViews?: readonly EditorWindow[] }) {
  const [revision, setRevision] = createSignal(0);
  const [anchor, setAnchor] = createSignal<HTMLElement | null>(null);
  const [palette, setPalette] = createSignal<ReturnType<typeof readWindowPalette>>(null);
  const [busy, setBusy] = createSignal<string | null>(null);
  let alive = true;
  onCleanup(() => { alive = false; });
  onMount(() => {
    const root = document.documentElement;
    const refresh = () => setPalette(() => readWindowPalette(root));
    refresh();
    onCleanup(watchPaletteTheme(root, refresh));
  });
  createEffect(() => {
    const replica = props.replica;
    setRevision(value => value + 1);
    if (replica) onCleanup(replica.subscribe(() => setRevision(value => value + 1)));
  });
  const windows = createMemo(() => {
    revision();
    const views = itemWindowViews();
    const rows = new Map<string, EditorWindow>();
    for (const participant of props.replica?.accepted.participants ?? []) {
      const window = editorWindow(participant, views);
      if (window) rows.set(window.clientId, window);
    }
    // Native file windows remain listed while loading or viewing history.
    for (const view of props.peerViews ?? []) {
      if (!rows.has(view.clientId)) rows.set(view.clientId, view);
    }
    const clientId = props.replica?.clientId ?? clientIdentity();
    const current = editorWindow({ client_id: clientId }, views);
    if (current && !rows.has(clientId)) rows.set(clientId, current);
    return [...rows.values()].sort((a, b) => a.slot - b.slot || a.clientId.localeCompare(b.clientId));
  }, undefined, { equals: (a, b) => a.length === b.length && a.every((window, index) => {
    const other = b[index];
    return other?.clientId === window.clientId && other.label === window.label && other.slot === window.slot
      && other.nativeLabel === window.nativeLabel && other.title === window.title;
  }) });
  const isCurrent = (id: string) => id === (props.replica?.clientId ?? clientIdentity());
  createEffect(() => { if (windows().length < 2) setAnchor(null); });
  const operate = async (id: string, action: () => Promise<boolean>) => {
    if (busy()) return;
    setBusy(id);
    try {
      const done = await action();
      if (!alive) return;
      if (done) setAnchor(null);
      else reportSurfaceFailure(WINDOW_UNAVAILABLE, "That window is no longer available.", props.projectId);
    } finally { if (alive) setBusy(null); }
  };
  return <Show when={windows().length > 1}>
    <button type="button" class="den-files-editor__status-toggle den-document-windows" data-testid="document-windows"
      aria-label={`Open in ${windows().length} windows`} aria-haspopup="menu" aria-expanded={anchor() !== null}
      onClick={event => { setAnchor(anchor() ? null : event.currentTarget); }}>
      <ThemeIcon slot="new-window" />
      <span class="den-document-windows__long">Open in </span>{windows().length}<span class="den-document-windows__long"> windows</span>
    </button>
    <Show when={anchor()}>
      <AnchoredSurface anchor={anchor} preferredSide="top" align="start" role="menu" ariaLabel="Windows with this file open"
        class="den-menu-surface den-document-windows__popover" onDismiss={() => setAnchor(null)} dismissWhenAnchorHidden>
        <div class="den-document-windows__heading">Open windows</div>
        <For each={windows()}>{window => <div class="den-document-windows__row" style={windowColorStyle(palette()?.(window.slot))}>
          <span class="den-document-windows__swatch" aria-hidden="true" />
          <button type="button" class="den-document-windows__focus" role="menuitem" disabled={isCurrent(window.clientId) || !window.nativeLabel || !!busy()}
            aria-label={isCurrent(window.clientId) ? `${window.label}, this window` : `Focus ${window.label.toLowerCase()}`}
            onClick={() => void operate(window.clientId, () => focusEditorWindow(window))}>
            <span>{window.label}</span>
            <span class="den-document-windows__detail">{isCurrent(window.clientId) ? "This window" : window.title || (window.nativeLabel ? "Desktop window" : "Open in browser")}</span>
          </button>
          <Show when={!isCurrent(window.clientId) && window.nativeLabel && window.nativeLabel !== "main"}>
            <button type="button" class="den-inset-icon-btn" role="menuitem" aria-label={`Close ${window.label.toLowerCase()}`} disabled={!!busy()}
              onClick={() => void operate(window.clientId, () => closeItemWindow(window.nativeLabel ?? ""))}>
              <ThemeIcon slot="dismiss" />
            </button>
          </Show>
        </div>}</For>
      </AnchoredSurface>
    </Show>
  </Show>;
}
