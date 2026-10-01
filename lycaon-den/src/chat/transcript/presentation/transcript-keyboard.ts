import { createEffect, createSignal, onCleanup } from "solid-js";
import { registerCommandHandler } from "../../../shortcuts/dispatcher.ts";
import { focusRegion } from "../../../shortcuts/focus-region.ts";

type Row = { key: string; kind: string };
const isMessage = (row: Row) => ["user", "assistant", "draft", "pending_user", "fallback"].includes(row.kind);

/** The mounted message row with the highest transcript index; DOM order is not row order. */
export function lastTranscriptRow(root: ParentNode): HTMLElement | null {
  let last: HTMLElement | null = null;
  let lastIndex = -Infinity;
  for (const row of root.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")) {
    const index = Number(row.dataset.index);
    if (Number.isFinite(index) && index > lastIndex) {
      last = row;
      lastIndex = index;
    }
  }
  return last;
}

/** Navigation follows logical rows across virtual windows and older pages. */
export function createTranscriptKeyboard(options: {
  active: () => boolean;
  rows: () => readonly Row[];
  root: () => HTMLElement | null | undefined;
  viewport: () => HTMLElement | null;
  reveal: (index: number) => void;
  loadEarlier: (first: boolean) => Promise<void>;
  /** Extends presented history toward the live tail; `last` presents the tail itself. */
  loadLater: (last: boolean) => Promise<void>;
  reportError: (error: unknown) => void;
}) {
  const [focusedKey, setFocusedKey] = createSignal<string>();
  let revision = 0;
  let focusFrame = 0;
  onCleanup(() => { revision++; cancelAnimationFrame(focusFrame); });
  const navigate = async (direction: "first" | "last" | 1 | -1) => {
    const token = ++revision;
    const active = document.activeElement;
    const key = active instanceof HTMLElement ? active.closest<HTMLElement>("[data-msg-id]")?.dataset.msgId ?? focusedKey() : focusedKey();
    let rows = options.rows().filter(isMessage);
    let index = rows.findIndex(row => row.key === key);
    const atStart = direction === "first" || direction === -1 && index <= 0;
    const atEnd = direction === "last" || direction === 1 && index >= 0 && index === rows.length - 1;
    if (atStart || atEnd) {
      await (atStart ? options.loadEarlier(direction === "first") : options.loadLater(direction === "last"));
      if (token !== revision || !options.active() || document.activeElement !== active) return;
      rows = options.rows().filter(isMessage);
      index = rows.findIndex(row => row.key === key);
    }
    const next = direction === "first" ? 0 : direction === "last" ? rows.length - 1 : index < 0
      ? direction === 1 ? 0 : rows.length - 1 : Math.max(0, Math.min(rows.length - 1, index + direction));
    const row = rows[next];
    if (!row) return;
    setFocusedKey(row.key);
    options.reveal(options.rows().findIndex(candidate => candidate.key === row.key));
    // The viewport publishes its virtual range on the next paint.
    let remaining = 60;
    const focus = () => {
      if (token !== revision || !options.active()) return;
      if (document.activeElement !== active && document.activeElement !== options.viewport() && document.activeElement !== document.body) return;
      const target = [...(options.root()?.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]") ?? [])]
        .find(element => element.dataset.msgId === row.key);
      if (target) { target.focus({ preventScroll: true }); return; }
      if (--remaining > 0) focusFrame = requestAnimationFrame(focus);
    };
    cancelAnimationFrame(focusFrame);
    focusFrame = requestAnimationFrame(focus);
  };
  const run = (direction: Parameters<typeof navigate>[0]) => { void navigate(direction).catch(options.reportError); };
  createEffect(() => {
    if (!options.active()) { revision++; return; }
    for (const [id, direction] of [["chat.nextMessage", 1], ["chat.previousMessage", -1],
      ["chat.firstMessage", "first"], ["chat.lastMessage", "last"]] as const) {
      onCleanup(registerCommandHandler(id, () => run(direction)));
    }
  });
  return {
    bindRow(element: HTMLElement, key: string) {
      element.tabIndex = -1;
      const focused = () => setFocusedKey(key);
      element.addEventListener("focusin", focused);
      const keydown = (event: KeyboardEvent) => {
        if (event.target !== element || event.altKey || event.ctrlKey || event.metaKey) return;
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          event.preventDefault(); run(event.key === "ArrowDown" ? 1 : -1);
        } else if (event.key === "Escape") { event.preventDefault(); focusRegion("composer"); }
      };
      element.addEventListener("keydown", keydown);
      onCleanup(() => {
        if (element.contains(document.activeElement)) options.viewport()?.focus({ preventScroll: true });
        element.removeEventListener("focusin", focused);
        element.removeEventListener("keydown", keydown);
      });
    },
  };
}
