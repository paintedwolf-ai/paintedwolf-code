import { focusRegion } from "../../shortcuts/focus-region.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { For, Show, createEffect, createSignal, on, onCleanup } from "solid-js";
import type { QueueItem } from "../../api/types.ts";
import type { PendingSend } from "../../chat/send/pending-sends.ts";
import { focusWithoutScroll } from "../../platform/interaction/focus.ts";

type Props = {
  items: QueueItem[];
  pendingSends?: readonly PendingSend[];
  running: boolean;
  paused: boolean;
  sending: boolean;
  /** Suppresses the paused state during drag or edit. */
  arranging: boolean;
  onBeginArrange: () => void;
  onEndArrange: () => void;
  onSetPaused: (paused: boolean) => void;
  onFireNow: (itemId: string) => void;
  onRemove: (itemId: string) => void;
  onUpdate: (itemId: string, text: string) => void;
  onReorder: (orderedIds: string[]) => void;
  onLink: (itemIds: string[]) => void;
  onUnlink: (itemIds: string[]) => void;
  onSend: () => void;
  onCancelSend: () => void;
  /** A tool approval is holding the queued send. */
  sendWaitingOnApproval: boolean;
};

function linked(a: QueueItem | undefined, b: QueueItem | undefined): boolean {
  return !!a?.group_id && a.group_id === b?.group_id;
}

export function QueuePopover(props: Props) {
  const pending = () => (props.pendingSends ?? []).filter(
    (entry) => entry.kind === "queued_prompt" && !props.items.some((item) => item.id === entry.operationId),
  );
  const count = () => props.items.length + pending().length;
  const [open, setOpen] = createSignal(true);
  const [editingId, setEditingId] = createSignal<string | null>(null);
  const [dragIndex, setDragIndex] = createSignal<number | null>(null);
  const [dropIndex, setDropIndex] = createSignal<number | null>(null);

  const rowEls: (HTMLLIElement | undefined)[] = [];
  let rootEl: HTMLDivElement | undefined;
  let pillEl: HTMLButtonElement | undefined;
  const restoreRow = (id: string | null) => queueMicrotask(() => {
    const row = id ? [...(rootEl?.querySelectorAll<HTMLElement>("[data-queue-id]") ?? [])].find(el => el.dataset.queueId === id) : undefined;
    const target = row?.querySelector<HTMLElement>(".queue-row__text") ?? pillEl;
    if (target?.isConnected) focusWithoutScroll(target);
    else focusRegion("composer");
  });
  onCleanup(() => { if (rootEl?.contains(document.activeElement)) queueMicrotask(() => focusRegion("composer")); });
  let editEl: HTMLTextAreaElement | undefined;

  // An empty queue reopens when its first item arrives.
  createEffect(
    on(
      () => count() > 0,
      (active) => {
        if (active) setOpen(true);
      },
    ),
  );

  const statusText = () => {
    if (props.sending) {
      return props.sendWaitingOnApproval ? "Waiting on your approval" : "Sending";
    }
    if (props.paused && !props.arranging) return "Paused";
    if (props.running) return "Sends at turn end";
    return "Sends next";
  };

  const closeEdit = () => {
    if (editingId() == null) return;
    const id = editingId();
    const restore = document.activeElement === editEl;
    setEditingId(null);
    props.onEndArrange();
    if (restore) restoreRow(id);
  };

  const startEdit = (item: QueueItem) => {
    if (props.sending) return;
    if (editingId() === item.id) return;
    closeEdit();
    setEditingId(item.id);
    props.onBeginArrange();
    queueMicrotask(() => {
      focusWithoutScroll(editEl);
      editEl?.setSelectionRange(editEl.value.length, editEl.value.length);
    });
  };

  const saveEdit = (item: QueueItem) => {
    if (editingId() !== item.id) return;
    const value = editEl?.value.trim() ?? "";
    if (!value) {
      props.onRemove(item.id);
    } else if (value !== item.text) {
      props.onUpdate(item.id, value);
    }
    closeEdit();
  };

  const moveByKeyboard = (index: number, delta: number) => {
    if (props.sending) return;
    const target = index + delta;
    if (target < 0 || target >= props.items.length) return;
    const order = props.items.map((it) => it.id);
    const [moved] = order.splice(index, 1);
    if (moved === undefined) return;
    order.splice(target, 0, moved);
    props.onReorder(order);
  };

  const insertionIndexFromPointer = (clientY: number): number => {
    for (let i = 0; i < props.items.length; i++) {
      const el = rowEls[i];
      if (!el) continue;
      const rect = el.getBoundingClientRect();
      if (clientY < rect.top + rect.height / 2) return i;
    }
    return props.items.length;
  };

  const startDrag = (e: PointerEvent, index: number) => {
    if (props.sending) return;
    if (e.button !== 0) return;
    e.preventDefault();
    const grip = e.currentTarget as HTMLElement;
    grip.setPointerCapture(e.pointerId);
    setDragIndex(index);
    setDropIndex(null);
    props.onBeginArrange();

    const onMove = (ev: PointerEvent) => {
      setDropIndex(insertionIndexFromPointer(ev.clientY));
    };
    const finish = (commit: boolean) => {
      grip.removeEventListener("pointermove", onMove);
      grip.removeEventListener("pointerup", onUp);
      grip.removeEventListener("pointercancel", onCancel);
      const from = dragIndex();
      const to = dropIndex();
      setDragIndex(null);
      setDropIndex(null);
      if (commit && from != null && to != null && to !== from && to !== from + 1) {
        const order = props.items.map((it) => it.id);
        const [moved] = order.splice(from, 1);
        if (moved !== undefined) {
          order.splice(to > from ? to - 1 : to, 0, moved);
          props.onReorder(order);
        }
      }
      props.onEndArrange();
    };
    const onUp = () => finish(true);
    const onCancel = () => finish(false);
    grip.addEventListener("pointermove", onMove);
    grip.addEventListener("pointerup", onUp);
    grip.addEventListener("pointercancel", onCancel);
  };

  const rowDropClass = (i: number) => {
    const drop = dropIndex();
    if (drop == null || dragIndex() == null) return {};
    return {
      "queue-row--drop-before": drop === i,
      "queue-row--drop-after": drop === props.items.length && i === props.items.length - 1,
    };
  };

  const groupPosition = (i: number): string | undefined => {
    const it = props.items[i];
    if (!it?.group_id) return undefined;
    const prev = linked(props.items[i - 1], it);
    const next = linked(it, props.items[i + 1]);
    if (prev && next) return "mid";
    if (next) return "start";
    if (prev) return "end";
    return undefined;
  };

  return (
    <Show when={count() > 0}>
      <div ref={rootEl} class="queue-popover" data-testid="queue-popover">
        <Show when={open()}>
          <section
            class="queue-card"
            data-testid="queue-popover-card"
            aria-label="Message queue"
          >
            <header class="queue-card__head">
              <span class="queue-card__title">
                Queue
                <span class="den-status-mark">{count()}</span>
              </span>
              <span class="queue-card__status" data-testid="queue-status">
                {statusText()}
              </span>
              <div class="queue-card__head-actions">
                <button
                  type="button"
                  class="queue-card__icon-btn den-inset-icon-btn"
                  classList={{ "queue-card__icon-btn--active": props.paused }}
                  data-testid="queue-pause"
                  aria-pressed={props.paused}
                  disabled={props.sending || props.items.length === 0}
                  aria-label={props.paused ? "Resume queue" : "Pause queue"}
                  data-tip={
                    props.paused
                      ? "Resume — let the queue send"
                      : "Pause — keep messages queued"
                  }
                  data-tip-pos="below"
                  onClick={() => props.onSetPaused(!props.paused)}
                >
                  <Show when={props.paused} fallback={<ThemeIcon slot="pause" size={14} />}>
                    <ThemeIcon slot="play" size={14} />
                  </Show>
                </button>
                <Show
                  when={props.sending}
                  fallback={
                    <button
                      type="button"
                      class="queue-card__send"
                      data-testid="queue-send"
                      data-tip={
                        props.running ? "Send to the current turn" : "Send the queue now"
                      }
                      disabled={props.arranging || props.items.length === 0}
                      onClick={() => props.onSend()}
                    >
                      Send
                    </button>
                  }
                >
                  <button
                    type="button"
                    class="queue-card__send queue-card__send--cancel"
                    data-testid="queue-cancel-send"
                    data-tip={
                      props.sendWaitingOnApproval
                        ? "Answer the approval to let it through, or take it back"
                        : "Take the message back before the turn picks it up"
                    }
                    onClick={() => props.onCancelSend()}
                  >
                    Cancel send
                  </button>
                </Show>
                <button
                  type="button"
                  class="queue-card__icon-btn den-inset-icon-btn"
                  data-testid="queue-collapse"
                  aria-label="Collapse queue"
                  onClick={() => { setOpen(false); restoreRow(null); }}
                >
                  <ThemeIcon slot="chevron-down" size={14} />
                </button>
              </div>
            </header>
            <Scrollport
              class="queue-card__scroll"
              contentAs="ul"
              contentClass="queue-card__list"
              content={{ role: "list" }}
            >
                <For each={props.items}>
                  {(item, i) => (
                    <>
                      <li
                        ref={(el) => {
                          rowEls[i()] = el;
                        }}
                        class="queue-row"
                        classList={{
                          "queue-row--dragging": dragIndex() === i(),
                          ...rowDropClass(i()),
                        }}
                        data-group={groupPosition(i())}
                        data-testid="queue-item"
                        data-queue-id={item.id}
                      >
                        <button
                          type="button"
                          class="queue-row__grip"
                          data-testid="queue-grip"
                          disabled={props.sending}
                          aria-label={`Reorder message ${i() + 1} of ${count()}`}
                          data-tip="Drag to reorder (Arrow keys work too)"
                          onPointerDown={(e) => startDrag(e, i())}
                          onKeyDown={(e) => {
                            if (e.key === "ArrowUp") {
                              e.preventDefault();
                              moveByKeyboard(i(), -1);
                            } else if (e.key === "ArrowDown") {
                              e.preventDefault();
                              moveByKeyboard(i(), 1);
                            }
                          }}
                        >
                          <ThemeIcon slot="grip" size={14} />
                        </button>
                        <Show
                          when={editingId() === item.id}
                          fallback={
                            <button
                              type="button"
                              class="queue-row__text"
                              data-tip="Click to edit"
                              disabled={props.sending}
                              onClick={() => startEdit(item)}
                            >
                              {/* The inner span supplies the clamp box. */}
                              <span class="queue-row__label">{item.text}</span>
                            </button>
                          }
                        >
                          <textarea
                            ref={editEl}
                            class="queue-row__edit"
                            data-testid="queue-edit-input"
                            aria-label={`Queued message ${i() + 1}`}
                            rows={3}
                            value={item.text}
                            onKeyDown={(e) => {
                              if (e.key === "Enter" && !e.shiftKey) {
                                e.preventDefault();
                                saveEdit(item);
                              } else if (e.key === "Escape") {
                                e.preventDefault();
                                closeEdit();
                              }
                            }}
                            onBlur={() => saveEdit(item)}
                          />
                        </Show>
                        <div class="queue-row__actions">
                          <Show when={i() > 0}>
                            <button
                              type="button"
                              class="queue-row__action den-inset-icon-btn"
                              aria-label="Move to the front"
                              data-tip="Move to the front"
                              data-testid="queue-fire-now"
                              disabled={props.sending}
                              onClick={() => props.onFireNow(item.id)}
                            >
                              <ThemeIcon slot="send-next" size={14} />
                            </button>
                          </Show>
                          <button
                            type="button"
                            class="queue-row__action den-inset-icon-btn"
                            aria-label="Edit message"
                            data-testid="queue-edit"
                            disabled={props.sending}
                            onClick={() => startEdit(item)}
                          >
                            <ThemeIcon slot="edit" size={14} />
                          </button>
                          <button
                            type="button"
                            class="queue-row__action queue-row__action--danger den-inset-icon-btn"
                            aria-label="Remove from queue"
                            data-testid="queue-remove"
                            disabled={props.sending}
                            onClick={() => { const next = props.items[i() + 1]?.id ?? props.items[i() - 1]?.id ?? null; props.onRemove(item.id); restoreRow(next); }}
                          >
                            <ThemeIcon slot="delete" size={14} />
                          </button>
                        </div>
                      </li>
                      <Show when={props.items[i() + 1]}>
                        {(next) => {
                          const isLinked = () => linked(item, next());
                          return (
                            <li
                              class="queue-connector"
                              classList={{ "queue-connector--linked": isLinked() }}
                            >
                              <button
                                type="button"
                                class="queue-connector__toggle"
                                data-testid="queue-link-toggle"
                                disabled={props.sending}
                                aria-pressed={isLinked()}
                                aria-label={
                                  isLinked()
                                    ? "Unlink from next message"
                                    : "Link with next message"
                                }
                                data-tip={
                                  isLinked()
                                    ? "Linked — sends as one message. Click to unlink."
                                    : "Link with the next message — they send as one."
                                }
                                onClick={() =>
                                  isLinked()
                                    ? props.onUnlink([next().id])
                                    : props.onLink([item.id, next().id])
                                }
                              >
                                <ThemeIcon slot="chain" size={12} />
                                <span class="queue-connector__label">
                                  {isLinked() ? "Sends together" : "Link"}
                                </span>
                              </button>
                            </li>
                          );
                        }}
                      </Show>
                    </>
                  )}
                </For>
                <For each={pending()}>
                  {(entry) => (
                    <li class="queue-row" data-testid="queue-pending-item" aria-busy="true">
                      <span class="queue-row__text">
                        <span class="queue-row__label">{entry.text || entry.attachmentLabels?.join(", ")}</span>
                      </span>
                      <span class="queue-card__status">Adding to queue</span>
                    </li>
                  )}
                </For>
            </Scrollport>
          </section>
        </Show>
        <button
          type="button"
          ref={pillEl}
          class="queue-pill"
          data-testid="queue-pill"
          aria-expanded={open()}
          onClick={() => setOpen((v) => !v)}
        >
          <ThemeIcon slot="queue" size={13} />
          <span class="queue-pill__count">{count()}</span>
          <span class="queue-pill__label">{props.sending ? "sending" : "queued"}</span>
          <Show when={props.paused}>
            <span class="queue-pill__paused" data-tip="Queue paused">
              <ThemeIcon slot="pause" size={14} />
            </span>
          </Show>
        </button>
      </div>
    </Show>
  );
}
