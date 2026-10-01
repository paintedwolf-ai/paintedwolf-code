import * as Y from "yjs";
import { createEffect, createRoot } from "solid-js";
import { StateEffect, StateField, type Extension, type Range } from "@codemirror/state";
import { Decoration, EditorView, ViewPlugin, WidgetType, showTooltip, hoverTooltip, type DecorationSet, type Tooltip, type ViewUpdate } from "@codemirror/view";
import type { DocumentReplica } from "./document-replica.ts";
import { decodeUpdate } from "./document-outbox.ts";
import { editorWindow, type EditorWindow } from "../../platform/windows/editor-windows.ts";
import { itemWindowViews } from "../../platform/windows/item-windows.ts";
import { readWindowPalette, windowColorStyle, type WindowColor } from "../../contributions/window-color-palette.ts";
import { watchPaletteTheme } from "../../contributions/palette-cache.ts";
import { documentSelectionLayer } from "./document-presence-selection.ts";

import { overviewWindowSelections, setOverviewWindowSelections } from "../../components/source/annotations/overview-window-selections.ts";

const refreshPresence = StateEffect.define<null>();
const setLabels = StateEffect.define<readonly Tooltip[]>();
const labels = StateField.define<readonly Tooltip[]>({
  create: () => [],
  update: (value, transaction) => {
    for (const effect of transaction.effects) if (effect.is(setLabels)) return effect.value;
    return transaction.docChanged ? [] : value;
  },
  provide: field => showTooltip.computeN([field], state => state.field(field)),
});
const LABEL_HOLD_MS = 1600;

type PeerRange = { window: EditorWindow; anchor: number; head: number; primary: boolean; color: WindowColor | undefined };

function applyColor(element: HTMLElement, color: WindowColor | undefined): void {
  for (const [property, value] of Object.entries(windowColorStyle(color))) element.style.setProperty(property, value);
}

class ParticipantCaret extends WidgetType {
  constructor(readonly peer: PeerRange) { super(); }
  eq(other: ParticipantCaret): boolean {
    return this.peer.window.clientId === other.peer.window.clientId && this.peer.window.label === other.peer.window.label
      && this.peer.color?.caret === other.peer.color?.caret;
  }
  toDOM(): HTMLElement {
    const caret = document.createElement("span");
    caret.className = "cm-document-caret";
    caret.setAttribute("aria-label", this.peer.window.label);
    caret.dataset.windowClient = this.peer.window.clientId;
    applyColor(caret, this.peer.color);
    return caret;
  }
}

function tooltip(peer: PeerRange): Tooltip {
  return { pos: peer.head, above: true, strictSide: false, arrow: false,
    create: () => {
      const dom = document.createElement("div");
      dom.className = "cm-document-label";
      dom.textContent = peer.window.label;
      dom.dataset.windowClient = peer.window.clientId;
      applyColor(dom, peer.color);
      return { dom };
    } };
}

function peerRanges(replica: DocumentReplica, palette: ReturnType<typeof readWindowPalette>): PeerRange[] {
  const peers: PeerRange[] = [];
  for (const participant of replica.accepted.participants) {
    if (participant.client_id === replica.clientId) continue;
    const window = editorWindow(participant, itemWindowViews());
    if (!window) continue;
    for (const [index, selected] of participant.ranges.entries()) {
      try {
        const anchor = Y.createAbsolutePositionFromRelativePosition(Y.decodeRelativePosition(decodeUpdate(selected.anchor)), replica.doc);
        const head = Y.createAbsolutePositionFromRelativePosition(Y.decodeRelativePosition(decodeUpdate(selected.head)), replica.doc);
        if (!anchor || !head || anchor.type !== replica.text || head.type !== replica.text) continue;
        peers.push({ window, anchor: anchor.index, head: head.index, primary: participant.main === index, color: palette?.(window.slot) });
      } catch {
        // A relative position may arrive before the text it names.
      }
    }
  }
  return peers;
}

function decorations(peers: readonly PeerRange[]): DecorationSet {
  const ranges: Range<Decoration>[] = [];
  for (const peer of peers) {
    ranges.push(Decoration.widget({ widget: new ParticipantCaret(peer), side: 1 }).range(peer.head));
  }
  return Decoration.set(ranges, true);
}

/** Presence decorates the document without sharing selection, focus, or scroll. */
export function documentPresence(replica: DocumentReplica): Extension {
  const plugin = ViewPlugin.fromClass(class {
    decorations = Decoration.none;
    peers: PeerRange[] = [];
    private alive = true;
    private queued = false;
    private timer: ReturnType<typeof setTimeout> | undefined;
    private readonly recent = new Map<string, { signature: string; until: number }>();
    private readonly stops: (() => void)[] = [];
    private palette: ReturnType<typeof readWindowPalette>;
    constructor(readonly view: EditorView) {
      const root = view.dom.ownerDocument.documentElement;
      this.palette = readWindowPalette(root);
      this.stops.push(replica.subscribe(() => this.refresh()));
      this.stops.push(watchPaletteTheme(root, () => { this.palette = readWindowPalette(root); this.refresh(); }));
      this.stops.push(createRoot(dispose => {
        createEffect(() => { itemWindowViews(); this.refresh(); });
        return dispose;
      }));
      this.rebuild();
      this.refresh();
    }
    private refresh(): void {
      if (this.queued || !this.alive) return;
      this.queued = true;
      queueMicrotask(() => {
        this.queued = false;
        if (this.alive) this.view.dispatch({ effects: [refreshPresence.of(null),
          setOverviewWindowSelections.of(peerRanges(replica, this.palette).map(peer => ({
            clientId: peer.window.clientId, anchor: peer.anchor, head: peer.head, color: peer.color?.caret,
          }))),
        ] });
      });
    }
    private rebuild(): void {
      this.peers = peerRanges(replica, this.palette);
      this.decorations = decorations(this.peers);
      const present = new Set<string>();
      for (const participant of replica.accepted.participants) {
        if (participant.client_id === replica.clientId) continue;
        present.add(participant.client_id);
        const signature = JSON.stringify([participant.main, participant.ranges]);
        if (this.recent.get(participant.client_id)?.signature !== signature) {
          this.recent.set(participant.client_id, { signature, until: Date.now() + LABEL_HOLD_MS });
        }
      }
      for (const id of this.recent.keys()) if (!present.has(id)) this.recent.delete(id);
    }
    private publishLabels(): void {
      clearTimeout(this.timer);
      const now = Date.now();
      const shown = this.peers.filter(peer => peer.primary && (this.recent.get(peer.window.clientId)?.until ?? 0) > now);
      const next = shown.map(peer => tooltip(peer));
      queueMicrotask(() => { if (this.alive) this.view.dispatch({ effects: setLabels.of(next) }); });
      if (shown.length) {
        const expiry = Math.min(...shown.map(peer => this.recent.get(peer.window.clientId)?.until ?? now));
        this.timer = setTimeout(() => this.refresh(), Math.max(1, expiry - now));
      }
    }
    update(update: ViewUpdate): void {
      if (update.docChanged || update.transactions.some(tr => tr.effects.some(effect => effect.is(refreshPresence)))) {
        this.rebuild(); this.publishLabels();
      }
      if ((update.selectionSet || update.focusChanged || update.docChanged) && update.view.hasFocus) {
        replica.publishPresence(update.state.selection);
      }
    }
    destroy(): void {
      this.alive = false;
      clearTimeout(this.timer);
      for (const stop of this.stops) stop();
      replica.clearPresence();
    }
  }, { decorations: instance => instance.decorations });
  return [labels, overviewWindowSelections, plugin, documentSelectionLayer(view => view.plugin(plugin)?.peers ?? []),
    hoverTooltip((view, position) => {
    const peers = view.plugin(plugin)?.peers.filter(peer => position >= Math.min(peer.anchor, peer.head) && position <= Math.max(peer.anchor, peer.head));
    if (!peers?.length) return null;
    const unique = new Map(peers.map(peer => [peer.window.clientId, peer]));
    return [...unique.values()].map(tooltip);
  }, { hoverTime: 250 })];
}
