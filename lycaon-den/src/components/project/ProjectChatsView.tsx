import { contextAction } from "../context-actions.ts";
import { sessionTitle } from "../../chat/session/session-title.ts";
import { createResidentActivity } from "../../ui/resident-activity.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  on,
  onCleanup,
} from "solid-js";
import type { SessionListPage, SessionSummary } from "../../api/types.ts";
import type { AttentionIndex } from "../../attention/attention-model.ts";
import { attentionReasonLabel } from "../../attention/attention-model.ts";
import {
  getLycaonClient,
  onSessionEvent,
} from "../../platform/connection/app-connection.ts";
import { confirmChatDelete } from "../../session/confirm-chat-delete.ts";
import { exportSessionTranscript } from "../../settings/storage/data-backup-actions.ts";
import { sessionLifecycleMenuItems } from "../../session/session-lifecycle-menu.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { BrowseSegmented } from "../browse/BrowseSegmented.tsx";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";
import { ContextMenu, type ContextMenuAnchor } from "../ContextMenu.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { PinIcon } from "../primitives/PinIcon.tsx";
import { TablePager } from "../list/TablePager.tsx";

export const CHATS_PAGE_SIZE = 25;

const QUERY_DEBOUNCE_MS = 200;
const SSE_REFETCH_DEBOUNCE_MS = 400;

type ChatListTab = "active" | "archived";
type ChatSortKey = "title" | "created" | "activity";
type ChatSortDir = "asc" | "desc";

type Props = {
  projectId: string;
  appStore: AppStore;
  attention?: AttentionIndex;
  onOpenSession: (sessionId: string) => void;
  onSetArchived: (row: SessionSummary, archived: boolean) => Promise<boolean>;
  onSetPinned: (row: SessionSummary, pinned: boolean) => Promise<boolean>;
  onDeleteSession: (row: SessionSummary) => Promise<boolean>;
  /** Set when a routed jump (Crossbar go-to, card CTA) landed here. */
  back?: StageBack | null;
};

function sortGlyph(active: boolean, dir: ChatSortDir): string {
  if (!active) return "";
  return dir === "asc" ? "↑" : "↓";
}

/** Paged chat inventory for one project. */
export function ProjectChatsView(props: Props) {
  const [tab, setTab] = createSignal<ChatListTab>("active");
  const [queryInput, setQueryInput] = createSignal("");
  const [query, setQuery] = createSignal("");
  const [sortKey, setSortKey] = createSignal<ChatSortKey>("activity");
  const [sortDir, setSortDir] = createSignal<ChatSortDir>("desc");
  const [page, setPage] = createSignal(0);
  const [pageCursors, setPageCursors] = createSignal<string[]>([""]);
  const [selected, setSelected] = createSignal<ReadonlySet<string>>(new Set());
  const [menu, setMenu] = createSignal<{
    anchor: ContextMenuAnchor;
    row: SessionSummary;
  } | null>(null);

  let queryTimer: ReturnType<typeof setTimeout> | undefined;
  createEffect(
    on(queryInput, (raw) => {
      clearTimeout(queryTimer);
      queryTimer = setTimeout(() => setQuery(raw.trim()), QUERY_DEBOUNCE_MS);
    }),
  );
  onCleanup(() => clearTimeout(queryTimer));

  // Filters reset pagination and hidden selections.
  createEffect(
    on([tab, query, sortKey, sortDir, () => props.projectId], () => {
      setPage(0);
      setPageCursors([""]);
      setSelected(new Set<string>());
    }),
  );

  const fetchArgs = () => ({
    projectId: props.projectId,
    archived: tab() === "archived",
    q: query(),
    sort: sortKey(),
    order: sortDir(),
    cursor: pageCursors()[page()] ?? "",
    page: page(),
  });
  const chats = createSurfaceQuery({
    name: "project-chats",
    source: () => {
      props.appStore.state.sidecarStatus;
      const client = getLycaonClient();
      const args = fetchArgs();
      return client ? { client, key: JSON.stringify(args), args } : null;
    },
    scope: ({ args }) => args.projectId,
    load: ({ client, args }): Promise<SessionListPage> => client.listProjectSessions(args.projectId, {
      archived: args.archived, q: args.q || undefined, sort: args.sort, order: args.order,
      limit: CHATS_PAGE_SIZE, cursor: args.cursor || undefined,
    }),
  });
  const pageData = chats.value;
  const displayedArgs = () => chats.displayed()?.source.args;
  const displayedPage = () => displayedArgs()?.page ?? 0;
  const refetch = chats.refresh;

  let sseTimer: ReturnType<typeof setTimeout> | undefined;
  createResidentActivity(() => {
  const detachSse = onSessionEvent(() => {
    clearTimeout(sseTimer);
    sseTimer = setTimeout(() => void refetch(), SSE_REFETCH_DEBOUNCE_MS);
  });
  return () => {
    detachSse();
    clearTimeout(sseTimer);
  };
  });

  const rows = createMemo<SessionSummary[]>(() => pageData()?.sessions ?? []);
  const total = () => pageData()?.total ?? 0;
  const changePage = (next: number) => {
    if (chats.loading()) return;
    if (next > page()) {
      const cursor = pageData()?.next_cursor?.trim();
      if (!cursor) return;
      setPageCursors((held) => {
        const cursors = held.slice(0, page() + 1);
        cursors[next] = cursor;
        return cursors;
      });
    }
    setPage(next);
  };

  const toggleSort = (key: ChatSortKey) => {
    if (sortKey() === key) {
      setSortDir(sortDir() === "asc" ? "desc" : "asc");
      return;
    }
    setSortKey(key);
    setSortDir(key === "title" ? "asc" : "desc");
  };

  const selectedCount = () => selected().size;
  const pageAllSelected = () =>
    rows().length > 0 && rows().every((r) => selected().has(r.id));
  const toggleSelectPage = () => {
    const next = new Set(selected());
    if (pageAllSelected()) {
      for (const r of rows()) next.delete(r.id);
    } else {
      for (const r of rows()) next.add(r.id);
    }
    setSelected(next);
  };
  const toggleSelect = (id: string, on: boolean) => {
    const next = new Set(selected());
    if (on) next.add(id);
    else next.delete(id);
    setSelected(next);
  };
  const clearSelection = () => setSelected(new Set<string>());

  const afterMutation = () => {
    clearSelection();
    void refetch();
  };

  const deselect = (id: string) => {
    const next = new Set(selected());
    next.delete(id);
    setSelected(next);
  };

  /** Accepted rows are removed from the retry selection. */
  const runBatch = async (
    ids: readonly string[],
    apply: (row: SessionSummary) => Promise<boolean>,
  ) => {
    if (ids.length === 0) return;
    for (const id of ids) {
      const row = rows().find((candidate) => candidate.id === id);
      if (row && (await apply(row))) deselect(id);
    }
    void refetch();
  };

  const setArchived = (ids: readonly string[], archived: boolean) =>
    runBatch(ids, (row) => props.onSetArchived(row, archived));

  const setPinned = async (row: SessionSummary, pinned: boolean) => {
    await props.onSetPinned(row, pinned);
    afterMutation();
  };

  const deleteChats = async (ids: readonly string[]) => {
    if (ids.length === 0) return;
    if (!(await confirmChatDelete(ids.length))) return;
    await runBatch(ids, (row) => props.onDeleteSession(row));
  };

  const exportRow = (row: SessionSummary, format: "md" | "json") => {
    const client = getLycaonClient();
    if (!client) return;
    void exportSessionTranscript(client, row.id, format);
  };

  const openMenu = (e: MouseEvent, row: SessionSummary) => {
    e.preventDefault();
    setMenu({ anchor: { x: e.clientX, y: e.clientY }, row });
  };

  const rowTitle = (row: SessionSummary) => sessionTitle(row.title);
  const timeLabel = (iso: string) => {
    const ms = Date.parse(iso);
    return Number.isNaN(ms) ? "—" : formatRelativeTime(ms);
  };

  const sortButton = (key: ChatSortKey, label: string, numeric: boolean) => (
    <button
      type="button"
      class="den-list-sort"
      classList={{
        "den-list-sort--active": sortKey() === key,
        "den-list-sort--activity": numeric,
      }}
      aria-sort={
        sortKey() === key
          ? sortDir() === "asc"
            ? "ascending"
            : "descending"
          : "none"
      }
      data-testid={`project-chats-sort-${key}`}
      onClick={() => toggleSort(key)}
    >
      {label}
      <span class="den-list-sort-glyph" aria-hidden="true">
        {sortGlyph(sortKey() === key, sortDir())}
      </span>
    </button>
  );

  return (
    <BrowseStagePanel
      back={props.back}
      appStore={props.appStore}
      scrollHost="main"
      contentReady={() => pageData() != null || chats.error() != null}
      title="Chats"
      testId="project-chats-view"
      primary={
        <DenInput
          data-testid="project-chats-search"
          type="search"
          aria-label="Filter chats by title"
          placeholder="Filter by title…"
          value={queryInput()}
          onInput={(e) => setQueryInput(e.currentTarget.value)}
        />
      }
      chips={
        <BrowseSegmented
          ariaLabel="Chat set"
          testId="project-chats-set"
          value={tab()}
          onChange={(id) => setTab(id === "archived" ? "archived" : "active")}
          options={[
            { id: "active", label: "Active", testId: "project-chats-set-active" },
            { id: "archived", label: "Archived", testId: "project-chats-set-archived" },
          ]}
        />
      }
    >
      <div class="project-chats-wrap">
        <div class="den-list-panel project-chats" data-testid="project-chats-list">
        <div class="den-list-head">
          <label class="project-list-check">
            <DenCheckboxControl
              checked={pageAllSelected()}
              aria-label="Select all on this page"
              data-testid="project-chats-select-page"
              onChange={toggleSelectPage}
            />
          </label>
          {sortButton("title", "Title", false)}
          <span class="project-chats__head-count" aria-hidden="true">
            Messages
          </span>
          {sortButton("created", "Created", true)}
          {sortButton("activity", "Last activity", true)}
        </div>
        <Show when={selectedCount() > 0}>
          <div class="den-list-toolbar" data-testid="project-chats-bulkbar">
            <span class="den-list-toolbar-count">{selectedCount()} selected</span>
            <div class="den-list-toolbar-actions">
            <Show
              when={!displayedArgs()?.archived}
              fallback={
                <DenButton
                  variant="ghost"
                  compact
                  data-testid="project-chats-bulk-unarchive"
                  onClick={() => void setArchived([...selected()], false)}
                >
                  Unarchive
                </DenButton>
              }
            >
              <DenButton
                variant="ghost"
                compact
                data-testid="project-chats-bulk-archive"
                onClick={() => void setArchived([...selected()], true)}
              >
                Archive
              </DenButton>
            </Show>
            <DenButton
              variant="danger"
              compact
              data-testid="project-chats-bulk-delete"
              onClick={() => void deleteChats([...selected()])}
            >
              Delete…
            </DenButton>
            <DenButton
              variant="ghost"
              compact
              data-testid="project-chats-clear-selection"
              onClick={clearSelection}
            >
              Clear
            </DenButton>
            </div>
          </div>
        </Show>
        <Show
          when={rows().length > 0}
          fallback={
            // No empty-state flash while the first page is still loading.
            <Show when={chats.state().state === "loaded"}>
              <p class="project-chats__empty" data-testid="project-chats-empty">
                <Show
                  when={query() !== ""}
                  fallback={
                    tab() === "archived" ? "No archived chats." : "No chats yet."
                  }
                >
                  No chats match this filter.
                </Show>
              </p>
            </Show>
          }
        >
          <ul class="project-chats__rows">
            <For each={rows()}>
              {(row) => (
                <li
                  class="chat-list-row"
                  classList={{ "chat-list-row--selected": selected().has(row.id) }}
                  data-testid={`chat-list-row-${row.id}`}
                  onClick={() => props.onOpenSession(row.id)}
                  onContextMenu={(e) => openMenu(e, row)}
                >
                  <label
                    class="project-list-check"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <DenCheckboxControl
                      checked={selected().has(row.id)}
                      aria-label={`Select ${rowTitle(row)}`}
                      data-testid={`chat-list-select-${row.id}`}
                      onChange={(e) => toggleSelect(row.id, e.currentTarget.checked)}
                    />
                  </label>
                  <button
                    type="button"
                    class="chat-list-row__title"
                    data-testid={`chat-list-open-${row.id}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      props.onOpenSession(row.id);
                    }}
                  >
                    <Show when={props.attention?.get(row.id)} keyed>
                      {(att) => (
                        <span
                          class="focused-session-list__dot"
                          data-attention-class={att.class}
                          role="img"
                          aria-label={attentionReasonLabel(att)}
                          data-tip={attentionReasonLabel(att)}
                        />
                      )}
                    </Show>
                    <span class="chat-list-row__name">{rowTitle(row)}</span>
                    <Show when={row.pin_rank != null}>
                      <span
                        class="chat-list-row__pin"
                        aria-label="Pinned"
                        role="img"
                      >
                        <PinIcon size={10} />
                      </span>
                    </Show>
                  </button>
                  <span class="chat-list-row__count">{row.message_count}</span>
                  <span class="chat-list-row__activity">{timeLabel(row.created_at)}</span>
                  <span class="chat-list-row__activity">{timeLabel(row.activity_at)}</span>
                  <button
                    type="button"
                    class="chat-list-row__menu-btn den-inset-icon-btn"
                    aria-label="Chat actions"
                    aria-haspopup="menu"
                    aria-expanded={menu()?.row.id === row.id}
                    data-testid={`chat-list-menu-${row.id}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      openMenu(e, row);
                    }}
                  >
                    <ThemeIcon slot="more" size={15} />
                  </button>
                </li>
              )}
            </For>
          </ul>
        </Show>
        <TablePager
          testId="project-chats-pager"
          page={displayedPage()}
          loading={chats.loading()}
          pageSize={CHATS_PAGE_SIZE}
          total={total()}
          canNext={!!pageData()?.next_cursor}
          onPageChange={changePage}
          ariaLabel="Chats pagination"
        />
        </div>
      </div>
      <Show when={menu()} keyed>
        {(m) => (
          <ContextMenu
            anchor={m.anchor}
            onDismiss={() => setMenu(null)}
            items={[
              contextAction("open", {
                testId: "chat-list-menu-open",
                onSelect: () => props.onOpenSession(m.row.id),
              }),
              ...(tab() === "active"
                ? [
                    contextAction(m.row.pin_rank != null ? "unpin" : "pin", {
                      testId: "chat-list-menu-pin",
                      onSelect: () => void setPinned(m.row, m.row.pin_rank == null),
                    }),
                  ]
                : []),
              ...sessionLifecycleMenuItems({
                archived: tab() === "archived",
                exportDisabled: m.row.message_count === 0,
                onExport: (format) => exportRow(m.row, format),
                onToggleArchive: () => void setArchived([m.row.id], tab() === "active"),
                onDelete: () => void deleteChats([m.row.id]),
                testIdPrefix: "chat-list-menu",
              }),
            ]}
          />
        )}
      </Show>
    </BrowseStagePanel>
  );
}
