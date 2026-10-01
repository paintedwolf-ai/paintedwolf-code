import { windowMenuItems } from "../window-menu-items.ts";
import { contextAction } from "../context-actions.ts";
import { countForSession, countLabel } from "../../notices/notice-rollup.ts";
import type { NoticeIndex } from "../../notices/notice-store.ts";
import { Show, createEffect, createMemo, createSignal, on, onCleanup, type JSX } from "solid-js";
import type { AttentionIndex } from "../../attention/attention-model.ts";
import {
  ATTENTION_CLASS_LABEL,
  attentionReasonLabel,
} from "../../attention/attention-model.ts";
import type {
  SidebarChatRow,
  SidebarChatSections,
} from "../../store/projects-sidebar-model.ts";
import type { ChatListSort } from "../../../shared/app-state-types.ts";
import { RowPinButton, RowRenameButton } from "../ListRowActions.tsx";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "../ContextMenu.tsx";
import { InlineRenameInput } from "../inline-rename/InlineRenameInput.tsx";
import { KeyedIndex } from "../keyed-index.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { NavSelectionDot, navRow } from "./NavSelectionDot.tsx";
import { createHeldOrder, type HeldEntry } from "./held-order.ts";
import { pinnedDragOffset, pinnedDropIndex, pinnedRowShift } from "./pinned-reorder.ts";
import { createRowTravel, travelKey } from "./row-travel.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { exportSessionTranscript } from "../../settings/storage/data-backup-actions.ts";
import { useNow } from "../../time/now.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { sessionLifecycleMenuItems } from "../../session/session-lifecycle-menu.ts";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";

type ActiveChat = { projectId: string; sessionId: string | null };
type Group = "pinned" | "chats";

const SESSION_TITLE_MAX = 80;
/** Past this distance a press on a row is a drag, not a click. */
const DRAG_THRESHOLD_PX = 4;
/** Leaving the list by this much pulls the chat into its own window. */
const PULL_OFF_MARGIN_PX = 16;

const SORT_OPTIONS = [
  { sort: "created", label: "Created", description: "Newest first. Rows stay where they are." },
  { sort: "activity", label: "Last activity", description: "Most recent message first." },
  { sort: "title", label: "Title", description: "A to Z." },
] as const satisfies readonly { sort: ChatListSort; label: string; description: string }[];

type Props = {
  projectId: string;
  sections: SidebarChatSections;
  /** Host inventory has settled for this project. */
  loaded: boolean;
  /** Total active chats in the project, shown on the All chats entry. */
  totalChats: number;
  activeChat: ActiveChat | null;
  sort: ChatListSort;
  /** The listed rows already follow `sort`; a new order applies before anything holds it. */
  sortSettled: boolean;
  onSortChange: (sort: ChatListSort) => void;
  /** Session-id → attention row for the status dot. */
  attention?: AttentionIndex;
  /** Scope-keyed notice lookup, for the per-row count. */
  noticeIndex?: () => NoticeIndex;
  /** Whether the selected chat is visible in this window. */
  focused?: boolean;
  allChatsActive?: boolean;
  onSelectSession: (row: SidebarChatRow) => void;
  onRenameSession: (row: SidebarChatRow, title: string) => void | Promise<void>;
  onTogglePin: (row: SidebarChatRow, pinned: boolean) => void;
  /** Places a pinned chat at a 1-based position among the pins. */
  onMovePin: (row: SidebarChatRow, position: number) => void;
  onArchiveSession: (row: SidebarChatRow) => void;
  onDeleteSession: (row: SidebarChatRow) => void;
  /** Session ids that also have an additional window view. */
  sessionWindowIds?: ReadonlySet<string>;
  /** Host-reported peer window count per chat. */
  sessionWindowCounts?: ReadonlyMap<string, number>;
  /** Host-assigned view slots for each chat, ordered oldest first. */
  sessionWindowViewNumbers?: ReadonlyMap<string, readonly number[]>;
  onOpenInNewWindow?: (row: SidebarChatRow) => void;
  onRaiseWindow?: (row: SidebarChatRow, viewNumber?: number) => void;
  /** Close exactly one host-registered peer view for this chat. */
  onCloseWindow?: (row: SidebarChatRow, viewNumber: number) => void;
  onOpenAllChats: () => void;
};

type Drag = { sessionId: string; from: number; to: number; dy: number; pitch: number; count: number };

/** External reorders wait for pointer exit; direct actions apply immediately. */
export function FocusedSessionList(props: Props) {
  const now = useNow("minute");
  const isSelected = (row: SidebarChatRow) =>
    props.activeChat?.projectId === row.projectId &&
    props.activeChat?.sessionId === row.sessionId;
  const isActive = (row: SidebarChatRow) =>
    (props.focused ?? true) && isSelected(row);
  const hasWindowView = (row: SidebarChatRow) =>
    props.sessionWindowIds?.has(row.sessionId) === true;
  const windowViewCount = (row: SidebarChatRow) =>
    props.sessionWindowCounts?.get(row.sessionId) ?? (hasWindowView(row) ? 1 : 0);
  const windowViewNumbers = (row: SidebarChatRow) =>
    props.sessionWindowViewNumbers?.get(row.sessionId) ?? [];
  const raiseWindowView = (row: SidebarChatRow) => {
    const viewNumber = windowViewNumbers(row)[0];
    if (viewNumber === undefined) props.onRaiseWindow?.(row);
    else props.onRaiseWindow?.(row, viewNumber);
  };

  const [menu, setMenu] = createSignal<{
    anchor: ContextMenuAnchor;
    row: SidebarChatRow;
    group: Group;
  } | null>(null);
  const [sortMenu, setSortMenu] = createSignal<Element | null>(null);
  const [renamingId, setRenamingId] = createSignal<string | null>(null);
  const [pointerInside, setPointerInside] = createSignal(false);
  const [drag, setDrag] = createSignal<Drag | null>(null);

  const source = createMemo((): HeldEntry<SidebarChatRow>[] => [
    ...props.sections.pinned.map((row) => ({ key: row.sessionId, section: "pinned", item: row })),
    ...props.sections.chats.map((row) => ({ key: row.sessionId, section: "chats", item: row })),
  ]);
  const held = createHeldOrder(
    source,
    () =>
      props.sortSettled &&
      (pointerInside() || menu() !== null || sortMenu() !== null || drag() !== null || renamingId() !== null),
    (entry) => isSelected(entry.item),
  );
  const group = (section: Group) =>
    held.presented().filter((entry) => entry.section === section).map((entry) => entry.item);
  const pinned = createMemo(() => group("pinned"));
  const chats = createMemo(() => group("chats"));
  /** The person's own change shows at once, even under the pointer. */
  const applyNow = (change: () => void) => {
    change();
    held.rebase();
  };

  let pinnedList: HTMLUListElement | undefined;
  let chatsList: HTMLUListElement | undefined;
  const pinnedTravel = createRowTravel(() => pinnedList);
  const chatsTravel = createRowTravel(() => chatsList);
  onCleanup(() => {
    pinnedTravel.dispose();
    chatsTravel.dispose();
  });
  createEffect(on(() => pinned().map((row) => row.sessionId).join("\0"), () => pinnedTravel.moved()));
  createEffect(on(() => chats().map((row) => row.sessionId).join("\0"), () => chatsTravel.moved()));
  createEffect(() => {
    if (props.loaded && (chats().length > 0 || pinned().length > 0)) {
      requestFirstTimeTip("selected-chat");
    }
  });

  const activeIndex = (rows: readonly SidebarChatRow[]) => rows.findIndex(isActive);

  const openMenu = (e: MouseEvent, row: SidebarChatRow, rowGroup: Group) => {
    e.preventDefault();
    setMenu({ anchor: { x: e.clientX, y: e.clientY }, row, group: rowGroup });
  };

  const togglePin = (row: SidebarChatRow) => applyNow(() => props.onTogglePin(row, row.pinRank == null));
  const movePin = (row: SidebarChatRow, position: number) => applyNow(() => props.onMovePin(row, position));

  /** Drag direction selects pin reordering or window detachment. */
  const startRowGesture = (row: SidebarChatRow, rowGroup: Group, e: PointerEvent) => {
    if (e.button !== 0) return;
    const startX = e.clientX;
    const startY = e.clientY;
    const listEl = (e.currentTarget as HTMLElement).closest("ul");
    const listRect = listEl?.getBoundingClientRect();
    const rows = listEl ? [...listEl.querySelectorAll<HTMLElement>(":scope > li")] : [];
    const from = rows.findIndex((li) => li.contains(e.currentTarget as Node));
    const [first, second] = rows;
    const pitch = first && second ? second.offsetTop - first.offsetTop : 0;
    const canReorder = rowGroup === "pinned" && rows.length > 1 && from >= 0 && pitch > 0;
    let moved = false;

    const swallowNextClick = () => {
      const squash = (click: MouseEvent) => {
        click.preventDefault();
        click.stopPropagation();
      };
      window.addEventListener("click", squash, { capture: true, once: true });
      // A drop outside the row sends no click to swallow.
      setTimeout(() => window.removeEventListener("click", squash, { capture: true }), 0);
    };
    const teardown = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", finish);
      window.removeEventListener("pointercancel", cancel);
      window.removeEventListener("keydown", escape, true);
    };
    const cancel = () => {
      teardown();
      setDrag(null);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || !drag()) return;
      event.preventDefault();
      event.stopPropagation();
      cancel();
    };
    const move = (event: PointerEvent) => {
      const dx = event.clientX - startX;
      const dy = event.clientY - startY;
      const outside =
        listRect &&
        (event.clientX < listRect.left - PULL_OFF_MARGIN_PX ||
          event.clientX > listRect.right + PULL_OFF_MARGIN_PX ||
          event.clientY < listRect.top - PULL_OFF_MARGIN_PX ||
          event.clientY > listRect.bottom + PULL_OFF_MARGIN_PX);
      if (outside && props.onOpenInNewWindow && Math.abs(dx) + Math.abs(dy) >= 14) {
        cancel();
        swallowNextClick();
        props.onOpenInNewWindow(row);
        return;
      }
      if (!canReorder) return;
      if (!moved && Math.abs(dy) < DRAG_THRESHOLD_PX) return;
      moved = true;
      setDrag({
        sessionId: row.sessionId,
        from,
        to: pinnedDropIndex(from, dy, pitch, rows.length),
        dy,
        pitch,
        count: rows.length,
      });
    };
    const finish = () => {
      teardown();
      const settled = drag();
      setDrag(null);
      if (!moved) return;
      swallowNextClick();
      if (settled && settled.to !== settled.from) {
        pinnedTravel.settle();
        movePin(row, settled.to + 1);
      }
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", finish);
    window.addEventListener("pointercancel", cancel);
    window.addEventListener("keydown", escape, true);
  };

  const dragStyle = (row: SidebarChatRow, index: number): JSX.CSSProperties | undefined => {
    const d = drag();
    if (!d) return undefined;
    const offset = d.sessionId === row.sessionId
      ? pinnedDragOffset(d.from, d.dy, d.pitch, d.count)
      : pinnedRowShift(index, d.from, d.to, d.pitch);
    return offset === 0 ? undefined : { transform: `translateY(${offset}px)` };
  };

  const exportRow = (row: SidebarChatRow, format: "md" | "json") => {
    const client = getLycaonClient();
    if (!client) return;
    void exportSessionTranscript(client, row.sessionId, format);
  };

  const attentionFor = (row: SidebarChatRow) => props.attention?.get(row.sessionId);
  const noticeCountFor = (row: SidebarChatRow) =>
    props.noticeIndex ? countForSession(props.noticeIndex(), row.sessionId) : 0;

  const renderRow = (row: () => SidebarChatRow, index: () => number, rowGroup: Group) => (
    <li
      class="den-list-row--nav den-list-row"
      classList={{ "is-dragging": drag()?.sessionId === row().sessionId }}
      style={rowGroup === "pinned" ? dragStyle(row(), index()) : undefined}
      {...navRow(index())}
      {...travelKey(row().sessionId)}
    >
      <Show
        when={renamingId() !== row().sessionId}
        fallback={
          <InlineRenameInput
            class="focused-session-list__rename"
            testId="session-rename-input"
            initialValue={row().title}
            maxLength={SESSION_TITLE_MAX}
            ariaLabel="Rename chat"
            onCommit={(next) => {
              setRenamingId(null);
              void props.onRenameSession(row(), next);
            }}
            onCancel={() => setRenamingId(null)}
          />
        }
      >
        <button
          type="button"
          class="focused-session-list__row den-shell-nav-sub-link"
          data-project-id={row().projectId}
          data-session-id={row().sessionId}
          aria-current={isSelected(row()) ? "true" : undefined}
          classList={{
            "den-shell-nav-sub-link-active": isSelected(row()),
            "focused-session-list__row--active": isSelected(row()),
          }}
          onClick={() => {
            props.onSelectSession(row());
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              const container = e.currentTarget.closest(".focused-session-list");
              const rows = Array.from(container?.querySelectorAll<HTMLButtonElement>(".focused-session-list__row") ?? []);
              const idx = rows.indexOf(e.currentTarget);
              if (idx >= 0 && idx + 1 < rows.length) {
                rows[idx + 1]?.focus();
              }
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              const container = e.currentTarget.closest(".focused-session-list");
              const rows = Array.from(container?.querySelectorAll<HTMLButtonElement>(".focused-session-list__row") ?? []);
              const idx = rows.indexOf(e.currentTarget);
              if (idx > 0) {
                rows[idx - 1]?.focus();
              }
            } else if (e.key === "Enter") {
              e.preventDefault();
              props.onSelectSession(row());
              focusRegion("composer");
            } else if (e.key === "Escape") {
              e.preventDefault();
              focusRegion("composer");
            }
          }}
          onDblClick={(e) => {
            e.preventDefault();
            setRenamingId(row().sessionId);
          }}
          onContextMenu={(e) => openMenu(e, row(), rowGroup)}
          onPointerDown={(e) => startRowGesture(row(), rowGroup, e)}
        >
          <span class="focused-session-list__title">{row().title}</span>
          <Show when={isSelected(row())}>
            <span class="sr-only">
              {isActive(row())
                ? "Selected chat, visible."
                : "Selected chat. Activate to show the conversation."}
            </span>
          </Show>
          <Show when={!isSelected(row()) && noticeCountFor(row()) > 0}>
            <span
              class="focused-session-list__notices"
              data-testid="session-notice-count"
              data-tip={`${noticeCountFor(row())} unresolved in this chat`}
            >
              {countLabel(noticeCountFor(row()))}
            </span>
          </Show>
          <Show
            when={!isSelected(row()) && attentionFor(row())}
            keyed
            fallback={
              <Show when={row().activityAtMs > 0 && !isSelected(row())}>
                <span class="focused-session-list__time" aria-hidden="true">
                  {formatRelativeTime(row().activityAtMs, now())}
                </span>
              </Show>
            }
          >
            {(att) => (
              <span
                class="focused-session-list__status"
                data-attention-class={att.class}
                data-tip={attentionReasonLabel(att)}
              >
                {ATTENTION_CLASS_LABEL[att.class]}
              </span>
            )}
          </Show>
        </button>
        <Show when={hasWindowView(row())}>
          <button
            type="button"
            class="focused-session-list__window den-inset-icon-btn"
            aria-label={`${windowViewCount(row())} open window view${windowViewCount(row()) === 1 ? "" : "s"}`}
            data-tip={`${windowViewCount(row())} open window view${windowViewCount(row()) === 1 ? "" : "s"}`}
            data-testid="session-window-glyph"
            onClick={() => raiseWindowView(row())}
          >
            ↗
            <Show when={windowViewCount(row()) > 1}>
              <span aria-hidden="true">{windowViewCount(row())}</span>
            </Show>
          </button>
        </Show>
        <div class="den-list-row__actions">
          <RowPinButton
            pinned={row().pinRank != null}
            testId="session-pin-action"
            onToggle={() => togglePin(row())}
          />
          <RowRenameButton
            label="Rename chat"
            testId="session-rename-action"
            onRename={() => setRenamingId(row().sessionId)}
          />
        </div>
      </Show>
    </li>
  );

  const menuItems = (row: SidebarChatRow, rowGroup: Group): ContextMenuItem[] => {
    const views = windowViewNumbers(row);
    const openInNewWindow = props.onOpenInNewWindow;
    const raiseWindow = props.onRaiseWindow;
    const closeWindow = props.onCloseWindow;
    const items: ContextMenuItem[] = [
      contextAction("open", {
        testId: "session-menu-open",
        onSelect: () => props.onSelectSession(row),
      }),
    ];
    items.push(...windowMenuItems({
      views: views.map((number) => ({ id: number, number })), testIdPrefix: "session-menu",
      open: openInNewWindow ? () => openInNewWindow(row) : undefined,
      focus: raiseWindow ? (number) => raiseWindow(row, number) : undefined,
      close: closeWindow ? (number) => closeWindow(row, number) : undefined,
    }));
    items.push(
      contextAction("rename", {
        testId: "session-menu-rename",
        onSelect: () => setRenamingId(row.sessionId),
      }),
      contextAction(row.pinRank != null ? "unpin" : "pin", {
        testId: "session-menu-pin",
        onSelect: () => togglePin(row),
      }),
    );
    if (rowGroup === "pinned") {
      const position = pinned().findIndex((r) => r.sessionId === row.sessionId) + 1;
      items.push(
        contextAction("moveUp", {
          testId: "session-menu-move-up",
          disabled: position <= 1,
          onSelect: () => movePin(row, position - 1),
        }),
        contextAction("moveDown", {
          testId: "session-menu-move-down",
          disabled: position >= pinned().length,
          onSelect: () => movePin(row, position + 1),
        }),
      );
    }
    items.push(
      contextAction("copyTitle", {
        testId: "session-menu-copy-title",
        onSelect: () => copyTextToClipboard(row.title),
      }),
      contextAction("copySessionID", {
        testId: "session-menu-copy-id",
        onSelect: () => copyTextToClipboard(row.sessionId),
      }),
      ...sessionLifecycleMenuItems({
        archived: false,
        exportDisabled: row.messageCount === 0,
        onExport: (format) => exportRow(row, format),
        onToggleArchive: () => applyNow(() => props.onArchiveSession(row)),
        onDelete: () => applyNow(() => props.onDeleteSession(row)),
        testIdPrefix: "session-menu",
      }),
    );
    return items;
  };

  const sortLabel = () =>
    SORT_OPTIONS.find((option) => option.sort === props.sort)?.label ?? SORT_OPTIONS[0].label;

  return (
    <div
      class="focused-session-list"
      data-testid="focused-session-list"
      data-first-time-tip-anchor="selected-chat"
      onPointerEnter={() => setPointerInside(true)}
      onPointerLeave={() => setPointerInside(false)}
    >
      <Show when={pinned().length > 0}>
        <h2 class="focused-session-list__heading">Pinned</h2>
        <div class="focused-session-list__branch">
          <ul
            ref={pinnedList}
            class="focused-session-list__list"
            classList={{ "focused-session-list__list--dragging": drag() !== null }}
            data-testid="focused-session-list-pinned"
          >
            <NavSelectionDot index={activeIndex(pinned())} />
            <KeyedIndex each={pinned()} keyOf={(row) => row.sessionId}>
              {(row, index) => renderRow(row, index, "pinned")}
            </KeyedIndex>
          </ul>
        </div>
      </Show>
      <div class="focused-session-list__header">
        <h2 class="focused-session-list__heading">Chats</h2>
        <button
          type="button"
          class="focused-session-list__sort"
          data-testid="focused-session-list-sort"
          aria-haspopup="menu"
          aria-expanded={sortMenu() !== null}
          aria-label={`Sort chats: ${sortLabel()}`}
          onClick={(e) => setSortMenu(sortMenu() ? null : e.currentTarget)}
        >
          <span>{sortLabel()}</span>
          <ThemeIcon slot="chevron-down" size={10} />
        </button>
      </div>
      <div class="focused-session-list__branch">
        <Show
          when={props.loaded && chats().length > 0}
          fallback={
            // Reserve the row until sessions load; pins alone need no empty line.
            <Show when={pinned().length === 0}>
              <p class="focused-session-list__empty" data-testid="focused-session-list-empty">
                {props.loaded ? "No chats yet" : ""}
              </p>
            </Show>
          }
        >
          <ul ref={chatsList} class="focused-session-list__list" data-testid="focused-session-list-chats">
            <NavSelectionDot index={activeIndex(chats())} />
            <KeyedIndex each={chats()} keyOf={(row) => row.sessionId}>
              {(row, index) => renderRow(row, index, "chats")}
            </KeyedIndex>
          </ul>
        </Show>
        <button
          type="button"
          class="focused-session-list__all den-shell-nav-sub-link"
          classList={{ "den-shell-nav-sub-link-active": props.allChatsActive === true }}
          data-testid="all-chats-entry"
          aria-current={props.allChatsActive === true ? "page" : undefined}
          onClick={() => props.onOpenAllChats()}
        >
          <span class="focused-session-list__all-label">All chats</span>
          <Show when={props.totalChats > 0}>
            <span class="focused-session-list__all-count">{props.totalChats}</span>
          </Show>
        </button>
      </div>
      <Show when={menu()} keyed>
        {(m) => (
          <ContextMenu
            anchor={m.anchor}
            onDismiss={() => setMenu(null)}
            items={menuItems(m.row, m.group)}
          />
        )}
      </Show>
      <Show when={sortMenu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            align="end"
            gap={4}
            label="Sort chats"
            items={[
              { label: "Sort chats", header: true },
              ...SORT_OPTIONS.map((option) => ({
                label: option.label,
                description: option.description,
                checked: props.sort === option.sort,
                testId: `focused-session-list-sort-${option.sort}`,
                onSelect: () => applyNow(() => props.onSortChange(option.sort)),
              })),
            ]}
            onDismiss={() => setSortMenu(null)}
          />
        )}
      </Show>
    </div>
  );
}
