import { batch, createEffect, createMemo, createSignal, on, untrack } from "solid-js";
import type {
  FindingIgnoreEntry,
  FindingLedgerEntry,
  FindingLedgerQueryRequest,
  FindingLedgerSort,
  FindingLedgerState,
  FindingLevel,
} from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { createDebounced } from "../../ui/debounced.ts";
import { FINDINGS_PAGE_SIZE } from "../../lib/scan-findings-table.ts";
import { ledgerEntryKey } from "../../lib/ledger-display.ts";
import type { SortDirection, SortState } from "../../list/list-sort.ts";

/** Project finding ledger with store-side filtering, ordering, and paging. */

export type LedgerOptions = {
  client: () => LycaonClient | undefined;
  projectId: () => string | undefined;
};

export type FindingLedgerModel = ReturnType<typeof createFindingLedger>;

/** Open: reported and not ignored. All: every recorded finding. */
export type LedgerTab = "open" | "all";

/** States displayed in the Open tab. */
const OPEN_STATES: FindingLedgerState[] = ["open", "reopened"];

/** Sort columns supported by the findings query. */
const SORT_KEYS = new Set<string>([
  "severity",
  "finding",
  "location",
  "state",
  "first_seen",
  "last_seen",
]);

function toLedgerSort(sort: SortState): {
  sort: FindingLedgerSort;
  order: SortDirection;
} {
  if (sort && SORT_KEYS.has(sort.key)) {
    return { sort: sort.key as FindingLedgerSort, order: sort.dir };
  }
  return { sort: "severity", order: "desc" };
}

export function createFindingLedger(options: LedgerOptions) {
  // Debounce filter text input.
  const [text, setText] = createSignal("");
  const [settledText, setSettledText] = createSignal("");
  const [tab, setTabSignal] = createSignal<LedgerTab>("open");
  const [levels, setLevels] = createSignal<ReadonlySet<FindingLevel>>(new Set());
  const [states, setStates] = createSignal<ReadonlySet<FindingLedgerState>>(new Set());
  const [scannerIds, setScannerIds] = createSignal<ReadonlySet<string>>(new Set());
  const [introducedSince, setIntroducedSince] = createSignal<string | undefined>(undefined);
  // Set by an outside focus request; shown as an active filter so it can be cleared.
  const [fingerprint, setFingerprint] = createSignal<string | undefined>(undefined);
  const [sort, setSort] = createSignal<SortState>({ key: "severity", dir: "desc" });
  const [page, setPage] = createSignal(0);
  const [pageCursors, setPageCursors] = createSignal<(string | undefined)[]>([undefined]);
  const [selected, setSelected] = createSignal<ReadonlySet<string>>(new Set());
  const [ignorePending, setIgnorePending] = createSignal(false);
  const [ignoreError, setIgnoreError] = createSignal<string | null>(null);

  const request = createMemo<FindingLedgerQueryRequest>(() => {
    const ordering = toLedgerSort(sort());
    const req: FindingLedgerQueryRequest = {
      limit: FINDINGS_PAGE_SIZE,
      cursor: pageCursors()[page()],
      sort: ordering.sort,
      order: ordering.order,
    };
    const query = settledText().trim();
    if (query) req.text = query;
    if (levels().size > 0) req.levels = [...levels()];
    // Explicit filter overrides tab default.
    if (states().size > 0) req.states = [...states()];
    else if (tab() === "open") req.states = [...OPEN_STATES];
    if (scannerIds().size > 0) req.scanner_ids = [...scannerIds()];
    const since = introducedSince();
    if (since) req.introduced_since_at = since;
    const only = fingerprint();
    if (only) req.fingerprint = only;
    return req;
  });

  const query = createSurfaceQuery({
    name: "project-findings",
    source: () => {
      const client = options.client();
      const projectId = options.projectId()?.trim();
      if (!client || !projectId) return null;
      const req = request();
      return { client, projectId, req, key: `${projectId}:${JSON.stringify(req)}` };
    },
    scope: ({ projectId }) => projectId,
    load: ({ client, projectId, req }) => client.queryProjectFindings(projectId, req),
    required: false,
  });

  // Catalog entries list independently of matches.
  const ignores = createSurfaceQuery({
    name: "project-finding-ignores",
    source: () => {
      const client = options.client();
      const projectId = options.projectId()?.trim();
      return client && projectId ? { client, projectId, key: projectId } : null;
    },
    scope: ({ projectId }) => projectId,
    load: ({ client, projectId }) => client.listProjectFindingIgnores(projectId),
    required: false,
  });

  const entries = (): FindingLedgerEntry[] => query.value()?.entries ?? [];
  /** Active request for currently displayed rows. */
  const shownRequest = createMemo(() => query.displayed()?.source.req);
  const shownKey = createMemo(() => query.displayed()?.source.key);
  const shownPage = () => {
    const shown = shownRequest();
    if (!shown) return page();
    const cursor = shown.cursor;
    const index = pageCursors().indexOf(cursor);
    return index >= 0 ? index : page();
  };
  const totalMatch = () => query.value()?.total_match ?? 0;
  const counts = () => query.value()?.counts;
  const byLevel = () => query.value()?.by_level;
  const canNext = () => JSON.stringify(shownRequest()) === JSON.stringify(request()) && Boolean(query.value()?.next_cursor);

  /** Explicit user filters in effect (excluding the default open-tab filter). */
  const filtersActive = createMemo(
    () =>
      text().trim() !== "" ||
      levels().size > 0 ||
      states().size > 0 ||
      scannerIds().size > 0 ||
      introducedSince() !== undefined ||
      fingerprint() !== undefined,
  );

  /** Reset pagination and selection on filter change. */
  const restart = () =>
    batch(() => {
      setPage(0);
      setPageCursors([undefined]);
      setSelected(new Set<string>());
    });

  function toggleIn<T>(
    read: () => ReadonlySet<T>,
    write: (next: ReadonlySet<T>) => void,
    value: T,
  ) {
    const next = new Set(read());
    if (next.has(value)) next.delete(value);
    else next.add(value);
    batch(() => {
      write(next);
      restart();
    });
  }

  const settleText = createDebounced(() =>
    batch(() => {
      setSettledText(untrack(text));
      restart();
    }),
  );
  createEffect(on(text, settleText, { defer: true }));

  const clearFilters = () =>
    batch(() => {
      setText("");
      setSettledText("");
      setLevels(new Set<FindingLevel>());
      setStates(new Set<FindingLedgerState>());
      setScannerIds(new Set<string>());
      setIntroducedSince(undefined);
      setFingerprint(undefined);
      restart();
    });

  const selectedEntries = createMemo(() =>
    entries().filter((entry) => selected().has(ledgerEntryKey(entry))),
  );

  const allOnPageSelected = createMemo(() => {
    const rows = entries();
    return rows.length > 0 && rows.every((entry) => selected().has(ledgerEntryKey(entry)));
  });

  /** Refreshes queries after mutation. */
  const writeIgnores = async (write: (client: LycaonClient, projectId: string) => Promise<unknown>) => {
    const client = untrack(options.client);
    const projectId = untrack(options.projectId)?.trim();
    if (!client || !projectId) return false;
    setIgnorePending(true);
    setIgnoreError(null);
    try {
      await write(client, projectId);
      await Promise.all([query.refresh(), ignores.refresh()]);
      setSelected(new Set<string>());
      return true;
    } catch (err) {
      setIgnoreError(String(err));
      return false;
    } finally {
      setIgnorePending(false);
    }
  };

  return {
    query,
    ignores,
    /** Current query without pagination parameters. */
    exportQuery: (): FindingLedgerQueryRequest => {
      const { limit: _limit, cursor: _cursor, ...rest } = request();
      return rest;
    },
    entries,
    totalMatch,
    counts,
    byLevel,
    page,
    shownPage,
    shownKey,
    canNext,
    setPage: (next: number) => {
      const target = Math.max(0, Math.floor(next));
      if (target > page()) {
        if (target !== page() + 1 || !canNext()) return;
        const cursor = query.value()?.next_cursor?.trim();
        if (!cursor) return;
        batch(() => {
          setPageCursors((held) => {
            const cursors = held.slice(0, page() + 1);
            cursors[target] = cursor;
            return cursors;
          });
          setPage(target);
        });
        return;
      }
      setPage(target);
    },
    pageSize: FINDINGS_PAGE_SIZE,
    tab,
    setTab: (next: LedgerTab) =>
      batch(() => {
        setTabSignal(next);
        setStates(new Set<FindingLedgerState>());
        restart();
      }),
    text,
    setText,
    levels,
    toggleLevel: (level: FindingLevel) => toggleIn(levels, setLevels, level),
    states,
    toggleState: (state: FindingLedgerState) => toggleIn(states, setStates, state),
    scannerIds,
    toggleScanner: (id: string) => toggleIn(scannerIds, setScannerIds, id),
    fingerprint,
    setFingerprint: (only: string | undefined) =>
      batch(() => {
        setFingerprint(only);
        restart();
      }),
    introducedSince,
    setIntroducedSince: (since: string | undefined) =>
      batch(() => {
        setIntroducedSince(since);
        restart();
      }),
    sort,
    setSort: (next: SortState) =>
      batch(() => {
        setSort(next);
        restart();
      }),
    filtersActive,
    clearFilters,
    selected,
    selectedEntries,
    allOnPageSelected,
    toggleSelected: (entry: FindingLedgerEntry) => {
      const key = ledgerEntryKey(entry);
      const next = new Set(selected());
      if (next.has(key)) next.delete(key);
      else next.add(key);
      setSelected(next);
    },
    toggleSelectAllOnPage: () =>
      setSelected(
        allOnPageSelected() ? new Set<string>() : new Set(entries().map(ledgerEntryKey)),
      ),
    clearSelection: () => setSelected(new Set<string>()),
    // A fingerprint scope writes one entry per finding.
    addIgnores: (write: FindingIgnoreEntry[]) =>
      writeIgnores(async (client, projectId) => {
        for (const entry of write) {
          await client.createProjectFindingIgnore(projectId, entry);
        }
      }),
    removeIgnore: (entryId: string) =>
      writeIgnores((client, projectId) => client.deleteProjectFindingIgnore(projectId, entryId)),
    ignorePending,
    ignoreError,
    clearIgnoreError: () => setIgnoreError(null),
  };
}
