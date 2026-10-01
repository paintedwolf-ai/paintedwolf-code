import { batch } from "solid-js";
import { createProjectionStore } from "./projection-store.ts";
import { createStore, reconcile, unwrap } from "solid-js/store";
import type { LycaonClient } from "../api/client.ts";
import type { Session, SessionEvent, SessionSummary } from "../api/types.ts";
import {
  DEFAULT_CHAT_LIST_SORT,
  type ChatListSort,
} from "../../shared/app-state-types.ts";
import { SIDEBAR_CHATS_CAP, comparePinned } from "./projects-sidebar-model.ts";

export type ProjectSessionsState = {
  /** Project whose chats are loaded; null before a project is foregrounded. */
  projectId: string | null;
  /** Order the person chose for unpinned rows. */
  sort: ChatListSort;
  /** Order the installed unpinned rows were fetched in; differs from `sort`
   *  until the rows for a new order arrive. */
  rowsSort: ChatListSort;
  /** Every pinned chat plus the first unpinned chats in `sort` order. */
  rows: SessionSummary[];
  /** Active (non-archived) chats in the project, pinned or not. */
  total: number;
  loaded: boolean;
  error?: string;
};

/** Unpinned rows fetched past the visible cap, so a pin fills the list before the host answers. */
export const SIDEBAR_CHATS_FETCH_LIMIT = SIDEBAR_CHATS_CAP * 2;
/** Every pinned chat fits one page. */
const PINNED_FETCH_LIMIT = 200;

const REFRESH_DEBOUNCE_MS = 250;

type SidebarInventory = { rows: SessionSummary[]; total: number };

export type ProjectSessionsSseBus = {
  onSessionEvent: (cb: (ev: SessionEvent) => void) => () => void;
};

export type ProjectSessionsStore = ReturnType<typeof createProjectSessionsStore>;

/** The sidebar row a freshly created session shows as. */
export function sessionSummaryFromSession(session: Session): SessionSummary {
  return {
    id: session.id,
    project_id: session.project_id,
    owner_person_id: session.owner_person_id,
    title: session.title,
    posture: session.posture,
    status: session.status,
    ...(session.pin_rank != null ? { pin_rank: session.pin_rank } : {}),
    message_count: 0,
    created_at: session.created_at,
    activity_at: session.activity_at,
  };
}

/** The sidebar's chat inventory, refreshed by session events and local mutations. */
export function createProjectSessionsStore(
  getClient: () => LycaonClient | null,
  sse: ProjectSessionsSseBus,
) {
  const [state, setState] = createStore<ProjectSessionsState>({
    projectId: null,
    sort: DEFAULT_CHAT_LIST_SORT,
    rowsSort: DEFAULT_CHAT_LIST_SORT,
    rows: [],
    total: 0,
    loaded: false,
  });

  let generation = 0;
  let refreshTimer: ReturnType<typeof setTimeout> | undefined;
  /** Rows as the host last listed them, before pending edits. */
  let hostRows: SessionSummary[] = [];
  /** A person's edits shown over host rows until the host answers them. */
  const holds = new Map<string, { patch: Partial<SessionSummary>; token: object }>();
  const withHolds = (rows: readonly SessionSummary[]) =>
    rows.map((row) => {
      const hold = holds.get(row.id);
      return hold ? { ...row, ...hold.patch } : row;
    });
  const setRowFields = (sessionId: string, patch: Partial<SessionSummary>) => {
    const idx = state.rows.findIndex((r) => r.id === sessionId);
    if (idx >= 0) setState("rows", idx, patch);
  };
  let cacheClient: LycaonClient | null = null;
  const cache = createProjectionStore<SidebarInventory>(32);
  const cacheFor = (client: LycaonClient) => {
    if (cacheClient !== client) {
      cache.clear();
      cacheClient = client;
    }
    return cache;
  };
  const cacheKey = (projectId: string, sort: ChatListSort) => `${projectId}\0${sort}`;

  async function fetchInventory(
    client: LycaonClient,
    projectId: string,
    sort: ChatListSort,
  ): Promise<SidebarInventory> {
    const [pinned, chats] = await Promise.all([
      client.listProjectSessions(projectId, { pinned: true, sort: "pin", limit: PINNED_FETCH_LIMIT }),
      client.listProjectSessions(projectId, { pinned: false, sort, limit: SIDEBAR_CHATS_FETCH_LIMIT }),
    ]);
    // Two separate reads: a pin committing between them lists the chat in both.
    const seen = new Set<string>();
    const rows = [...pinned.sessions, ...chats.sessions].filter((row) => !seen.has(row.id) && seen.add(row.id));
    return { rows, total: pinned.total + chats.total };
  }

  async function load(projectId: string, sort: ChatListSort, refresh = false): Promise<void> {
    const client = getClient();
    if (!client) return;
    const gen = ++generation;
    const record = cacheFor(client).get(cacheKey(projectId, sort));
    if (refresh) record.invalidate();
    const release = record.retain();
    try {
      const inventory = await record.read(() => fetchInventory(client, projectId, sort));
      if (gen !== generation || state.projectId !== projectId || state.sort !== sort || client !== getClient()) return;
      batch(() => {
        if (inventory) {
          // Copies: the store mutates the row objects it is given.
          hostRows = inventory.rows.map((r) => ({ ...r }));
          setState("rows", reconcile(withHolds(inventory.rows), { key: "id" }));
          setState({ rowsSort: sort, total: inventory.total, loaded: true, error: undefined });
        } else {
          const result = record.state();
          if (result.state === "error") setState({ loaded: true, error: result.message });
        }
      });
    } finally {
      release();
      cache.trim();
    }
  }

  /** Shows the retained inventory for a project and order at once, then revalidates. */
  function present(projectId: string | null, sort: ChatListSort): void {
    generation++;
    clearTimeout(refreshTimer);
    const client = getClient();
    const retained = projectId && client ? cacheFor(client).get(cacheKey(projectId, sort)).value() : undefined;
    hostRows = (retained?.rows ?? []).map((r) => ({ ...r }));
    setState({
      projectId,
      sort,
      rowsSort: sort,
      rows: withHolds(retained?.rows ?? []),
      total: retained?.total ?? 0,
      loaded: retained !== undefined,
      error: undefined,
    });
    if (projectId) void load(projectId, sort, true);
  }

  function setProject(projectId: string | null): void {
    if (state.projectId === projectId) return;
    present(projectId, state.sort);
  }

  /** Pins and the current rows stay while the rows for the new order load. */
  function setSort(sort: ChatListSort): void {
    if (state.sort === sort) return;
    generation++;
    setState("sort", sort);
    if (state.projectId) void load(state.projectId, sort, true);
  }

  async function refresh(): Promise<void> {
    clearTimeout(refreshTimer);
    if (state.projectId) await load(state.projectId, state.sort, true);
  }

  function scheduleRefresh(): void {
    if (!state.projectId) return;
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(() => void refresh(), REFRESH_DEBOUNCE_MS);
  }

  sse.onSessionEvent(() => scheduleRefresh());

  function retainLocalRows(): void {
    const projectId = state.projectId;
    const client = getClient();
    if (!projectId || !client) return;
    const record = cacheFor(client).get(cacheKey(projectId, state.sort));
    record.publish({ rows: structuredClone(unwrap(state.rows)), total: state.total });
    record.invalidate();
  }

  /** Shows `patches` at once and over any reload until `send` settles; a failure
   *  restores the host's fields and rethrows. A newer patch to a row wins. */
  async function applyRowPatches(
    patches: ReadonlyMap<string, Partial<SessionSummary>>,
    send: () => Promise<unknown>,
  ): Promise<void> {
    const token = {};
    batch(() => {
      for (const [sessionId, patch] of patches) {
        holds.set(sessionId, { patch, token });
        setRowFields(sessionId, patch);
      }
    });
    const settle = (sessionId: string) => {
      if (holds.get(sessionId)?.token !== token) return false;
      holds.delete(sessionId);
      return true;
    };
    try {
      await send();
    } catch (error) {
      batch(() => {
        for (const [sessionId, patch] of patches) {
          if (!settle(sessionId)) continue;
          const host = hostRows.find((r) => r.id === sessionId);
          if (!host) continue;
          setRowFields(
            sessionId,
            Object.fromEntries(
              Object.keys(patch).map((key) => [key, host[key as keyof SessionSummary]]),
            ) as Partial<SessionSummary>,
          );
        }
      });
      throw error;
    }
    for (const sessionId of patches.keys()) settle(sessionId);
  }

  return {
    state,
    setProject,
    setSort,
    refresh,
    applyRowPatches,
    /** The rank a pin appended now would take. */
    nextPinRank(): number {
      return state.rows.reduce((max, row) => Math.max(max, row.pin_rank ?? 0), 0) + 1;
    },
    /** Pinned chats in pin order, as shown. */
    pinnedIds(): string[] {
      return state.rows.filter((row) => row.pin_rank != null).sort(comparePinned).map((row) => row.id);
    },
    /** A session this window just created shows at once; refresh reconciles after. */
    insertRow(row: SessionSummary): void {
      if (state.projectId !== row.project_id) return;
      if (state.rows.some((r) => r.id === row.id)) return;
      hostRows = [{ ...row }, ...hostRows];
      setState("rows", (rows) => [row, ...rows]);
      setState("total", (t) => t + 1);
      retainLocalRows();
    },
    /** Optimistic local removal (archive/delete); refresh reconciles after. */
    removeRow(sessionId: string): void {
      const idx = state.rows.findIndex((r) => r.id === sessionId);
      if (idx < 0) return;
      setState("rows", (rows) => rows.filter((r) => r.id !== sessionId));
      setState("total", (t) => Math.max(0, t - 1));
      retainLocalRows();
    },
  };
}
