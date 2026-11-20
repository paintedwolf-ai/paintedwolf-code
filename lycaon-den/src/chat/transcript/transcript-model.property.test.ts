// @vitest-environment jsdom
import { createMemo, createRoot } from "solid-js";
import { unwrap } from "solid-js/store";
import { afterEach, describe, expect, it } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { Message, MessageEvent, Session, SessionTranscriptPage } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { activitySpanEntryKey } from "../tool/activity-span-model.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { stubClient } from "../../test/client-fixture.ts";
import {
  clearSessionChatCacheForTests,
  getSessionChatCache,
  rememberSessionChatFromStore,
  restoreCachedSessionChat,
} from "../session/session-chat-cache.ts";
import { publishTranscriptReaderReach } from "./history/transcript-reader-reach.ts";
import { loadTranscriptPage } from "./layout/transcript-load-more.ts";
import {
  createTranscriptVirtualizer,
  transcriptOffsetForPosition,
  transcriptReadingPosition,
  type MeasuredTranscriptVirtualizer,
  type TranscriptAnchorResolution,
  type TranscriptReadingPosition,
} from "./layout/transcript-virtualizer.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  TRANSCRIPT_RESIDENT_ORD_BUDGET,
  TRANSCRIPT_RESIDENT_ORD_HARD_CAP,
  contiguousWindowRows,
  residentPageOrds,
  spanFromWireBounds,
  unloadedTranscriptOrds,
  type TranscriptWindow,
} from "./layout/transcript-window.ts";
import { ACTIVITY_SPAN_ENTRY_ESTIMATE_PX } from "./presentation/transcript-row-presentation.ts";
import {
  applyMessageEvent,
  completeChatHydrationAndReplay,
  replayForegroundBufferedMessages,
} from "./projection/message-events.ts";
import { bindTranscriptSpanAnchors } from "./projection/transcript-item-anchors.ts";
import type { TranscriptItem } from "./projection/transcript-item-model.ts";

/**
 * Seeded model test for the session transcript window. A host model holds each session's whole
 * transcript; random page, evict, regroup, resize, switch and restore steps interleave with live
 * appends and patches, and every step checks the store and the virtualizer against that model.
 * Replay one seed with -t "for seed <seed>$" or DEN_TRANSCRIPT_MODEL_SEED=<seed>;
 * DEN_TRANSCRIPT_MODEL_STEPS lengthens each run.
 */

const STEPS = Number(process.env.DEN_TRANSCRIPT_MODEL_STEPS) || 60;
const FIXED_SEEDS = [1, 2, 3, 7, 42, 1337, 0xdeadbeef, 0x5eed];
const LOOP_SEEDS = 16;
const ROW_PX = 72;
const ENTRY_PX = ACTIVITY_SPAN_ENTRY_ESTIMATE_PX;
const REACH_ORDS = 20;
const EDGE_ROWS = 5;

class Lcg {
  private state: number;
  constructor(seed: number) {
    this.state = (seed ^ 0x9e3779b9) >>> 0;
    // Nearby seeds start far apart.
    for (let i = 0; i < 4; i++) this.next();
  }
  next(): number {
    this.state = (Math.imul(this.state, 1664525) + 1013904223) >>> 0;
    return this.state / 4294967296;
  }
  int(n: number): number {
    return Math.floor(this.next() * n);
  }
  chance(p: number): boolean {
    return this.next() < p;
  }
  pick<T>(items: readonly T[]): T {
    return items[this.int(items.length)]!;
  }
}

function seedList(): number[] {
  const replay = Number(process.env.DEN_TRANSCRIPT_MODEL_SEED);
  if (Number.isInteger(replay) && process.env.DEN_TRANSCRIPT_MODEL_SEED) return [replay];
  const gen = new Lcg(0x7a11);
  return [...FIXED_SEEDS, ...Array.from({ length: LOOP_SEEDS }, () => gen.int(2 ** 31))];
}

type HostSession = { session: Session; rows: Message[]; seq: number; missed: MessageEvent[] };

const isTool = (row: Message) => row.content.startsWith("tool");
const ordOf = (id: string) => Number(id.slice(id.lastIndexOf("#") + 1));

function hostRow(sid: string, ord: number, seq: number, tool: boolean): Message {
  return {
    id: `${sid}#${ord}`,
    role: "user",
    origin: "user",
    authority: "user",
    trust_tier: "trusted",
    content: `${tool ? "tool" : "row"} ${ord} r${seq}`,
    created_at: "2026-01-01T00:00:00Z",
    ord,
    seq,
  };
}

function hostSession(rng: Lcg, sid: string, projectId: string): HostSession {
  const rows: Message[] = [];
  const count = 150 + rng.int(2400);
  let tool = false;
  for (let ord = 1; ord <= count; ord++) {
    tool = rng.chance(tool ? 0.6 : 0.2);
    rows.push(hostRow(sid, ord, ord, tool));
  }
  return {
    session: {
      id: sid,
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: projectId,
      workspace_path: `/tmp/${projectId}`,
      title: sid,
      posture: "build",
      status: "idle",
      created_at: "2026-01-01T00:00:00Z",
      activity_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
    rows,
    seq: count,
    missed: [],
  };
}

function hostPage(host: HostSession, start: number, end: number): SessionTranscriptPage {
  return {
    messages: structuredClone(host.rows.slice(start, end)),
    ...(start > 0 ? { before_cursor: `before-${start}` } : {}),
    ...(end < host.rows.length ? { after_cursor: `after-${end}` } : {}),
    watermark: host.seq,
    ord_bounds: { first_ord: 1, last_ord: host.rows.length },
    turn_clocks: {},
    turn_loads: {},
  } as SessionTranscriptPage;
}

/** The host's page query over the model: message-id cursors, an ordinal seek, or the newest rows. */
function hostQuery(
  host: HostSession,
  opts: { limit?: number; beforeMessageId?: string; afterMessageId?: string; afterOrd?: number } = {},
): SessionTranscriptPage {
  const n = host.rows.length;
  const limit = opts.limit ?? DEFAULT_TRANSCRIPT_PAGE_LIMIT;
  const indexOf = (id: string) => {
    const index = host.rows.findIndex((row) => row.id === id);
    if (index < 0) throw new Error(`host has no row ${id}`);
    return index;
  };
  if (opts.beforeMessageId) {
    const index = indexOf(opts.beforeMessageId);
    return hostPage(host, Math.max(0, index - limit), index);
  }
  if (opts.afterMessageId) {
    const index = indexOf(opts.afterMessageId) + 1;
    return hostPage(host, index, Math.min(n, index + limit));
  }
  if (opts.afterOrd !== undefined) return hostPage(host, opts.afterOrd, Math.min(n, opts.afterOrd + limit));
  return hostPage(host, Math.max(0, n - limit), n);
}

/** Consecutive tool rows form one activity span keyed by its first entry. */
function groupRows(rows: readonly Message[]): TranscriptItem[] {
  const items: TranscriptItem[] = [];
  let span: string[] = [];
  const flush = () => {
    if (span.length === 0) return;
    items.push({
      kind: "activity_span",
      key: span[0]!,
      label: "activity",
      entries: span.map((key) => ({ kind: "index_warming", key, meta: {} as never })),
    });
    span = [];
  };
  for (const row of rows) {
    if (isTool(row)) {
      span.push(row.id);
      continue;
    }
    flush();
    items.push({ kind: "user", key: row.id, text: row.content });
  }
  flush();
  return items;
}

function locate(items: readonly TranscriptItem[], id: string): { index: number; entry: number } | undefined {
  for (let index = 0; index < items.length; index++) {
    const item = items[index]!;
    if (item.kind === "activity_span") {
      const entry = item.entries.findIndex((e) => activitySpanEntryKey(e) === id);
      if (entry >= 0) return { index, entry };
    } else if (item.key === id) {
      return { index, entry: 0 };
    }
  }
  return undefined;
}

/** Rows the chat column presents: across a tail gap only the history that reads contiguously. */
function presentedRows(store: AppStore): readonly Message[] {
  const tw = store.state.transcript;
  return tw.hasTailGap ? contiguousWindowRows(tw) : store.state.messages;
}

type View = {
  virtualizer: MeasuredTranscriptVirtualizer;
  items: () => readonly TranscriptItem[];
  resolve: (rowKey: string) => TranscriptAnchorResolution | undefined;
  scrollTo: (offset: number) => void;
  settle: () => Promise<void>;
  dispose: () => void;
};

/** A mounted session virtualizer whose scroller moves only by the shifts the virtualizer applies. */
function mountView(store: AppStore): View {
  const el = document.createElement("div");
  Object.defineProperty(el, "clientHeight", { value: 400, configurable: true });
  Object.defineProperty(el, "clientWidth", { value: 400, configurable: true });
  Object.defineProperty(el, "scrollHeight", { value: 50_000_000, configurable: true });
  Object.defineProperty(el, "scrollTop", { value: 0, writable: true, configurable: true });
  document.body.appendChild(el);
  let domOffset = 0;
  let publishOffset: ((offset: number, isScrolling: boolean) => void) | undefined;
  let virtualizer!: MeasuredTranscriptVirtualizer;
  let items!: () => readonly TranscriptItem[];
  let resolve!: View["resolve"];
  const dispose = createRoot((d) => {
    items = createMemo(() => groupRows(presentedRows(store)));
    resolve = bindTranscriptSpanAnchors({ items, isOpen: () => true, rename: () => {} });
    virtualizer = createTranscriptVirtualizer({
      items,
      scrollElement: () => el,
      resolveLostAnchor: resolve,
      rowSizeHint: (item) => (item.kind === "activity_span" ? item.entries.length * ENTRY_PX : ROW_PX),
      contentRevision: (item) => (item.kind === "activity_span" ? String(item.entries.length) : item.kind),
      unloadedOrds: () => unloadedTranscriptOrds(store.state.transcript),
      revealOffset: (offset) => {
        domOffset = offset;
      },
      shiftContent: (delta) => {
        domOffset += delta;
        return delta;
      },
      observeElementRect: (_instance, cb) => {
        cb({ width: 400, height: 400 });
        return () => {};
      },
      observeElementOffset: (_instance, cb) => {
        publishOffset = cb;
        cb(domOffset, false);
        return () => {};
      },
    });
    return d;
  });
  return {
    virtualizer,
    items,
    resolve,
    scrollTo: (offset) => {
      domOffset = offset;
      publishOffset?.(offset, false);
    },
    // Deferred shifts land in microtasks; then the scroller reports where it is, as a scroll event would.
    settle: async () => {
      for (let i = 0; i < 6; i++) await Promise.resolve();
      publishOffset?.(domOffset, false);
    },
    dispose: () => {
      dispose();
      el.remove();
    },
  };
}

/** The row the reader reads and how far into that row or span entry the scrollport top sits. */
type Reader = { id: string; inRowPx: number };
type Saved = { transcript: TranscriptWindow; reader: Reader; position: TranscriptReadingPosition };

type Op =
  | "append" | "patch" | "regroup" | "background" | "page-older" | "page-newer"
  | "evict" | "resize" | "scroll" | "latest" | "switch" | "restore";

const OP_WEIGHTS: ReadonlyArray<readonly [Op, number]> = [
  ["append", 16], ["patch", 9], ["regroup", 6], ["background", 7], ["page-older", 16],
  ["page-newer", 10], ["evict", 6], ["resize", 8], ["scroll", 8], ["latest", 2],
  ["switch", 7], ["restore", 3],
];

class ModelRun {
  readonly rng: Lcg;
  readonly log: string[] = [];
  readonly hosts = new Map<string, HostSession>();
  readonly saved = new Map<string, Saved>();
  readonly store = createAppStore();
  readonly client: LycaonClient;
  current: string;
  project: string;
  view: View | undefined;
  reader: Reader | undefined;

  constructor(seed: number) {
    this.rng = new Lcg(seed);
    for (const [suffix, projectId] of [["a", "proj-1"], ["b", "proj-1"], ["c", "proj-2"]] as const) {
      const sid = `s${seed}-${suffix}`;
      this.hosts.set(sid, hostSession(this.rng, sid, projectId));
    }
    this.current = `s${seed}-a`;
    this.project = "proj-1";
    this.client = stubClient({
      listSessionMessages: async (sid: string, opts?: Parameters<typeof hostQuery>[1]) => {
        const page = hostQuery(this.hostOf(sid), opts);
        this.log.push(`  fetch ${JSON.stringify(opts)} -> ${page.messages[0]?.ord}..${page.messages.at(-1)?.ord}`);
        // SSE keeps arriving while the page request is in flight.
        if (this.rng.chance(0.4)) for (let k = 1 + this.rng.int(3); k > 0; k--) this.liveEvent(sid);
        return page;
      },
    });
  }

  hostOf(sid: string): HostSession {
    return this.hosts.get(sid)!;
  }

  get host(): HostSession {
    return this.hostOf(this.current);
  }

  presented(): Message[] {
    return unwrap(presentedRows(this.store)) as Message[];
  }

  deliver(sid: string, row: Message, op: "append" | "patch"): void {
    const event: MessageEvent = { session_id: sid, op, seq: row.seq, message: structuredClone(row) };
    // Events for another project's sessions resume from the event cursor on return.
    if (this.hostOf(sid).session.project_id === this.project) applyMessageEvent(this.store, event);
    else this.hostOf(sid).missed.push(event);
  }

  append(sid: string): void {
    const host = this.hostOf(sid);
    const last = host.rows.at(-1)!;
    const row = hostRow(sid, last.ord! + 1, ++host.seq, this.rng.chance(isTool(last) ? 0.6 : 0.2));
    host.rows.push(row);
    this.log.push(`  append ${row.id}${isTool(row) ? " tool" : ""}`);
    this.deliver(sid, row, "append");
  }

  patch(sid: string, regroup: boolean): void {
    const host = this.hostOf(sid);
    const resident = this.store.state.transcriptSessionId === sid ? this.presented() : [];
    let pool: readonly Message[] = resident.length > 0 && this.rng.chance(0.7) ? resident : host.rows;
    if (regroup) {
      // A plain row that gains tool calls joins the activity beside it.
      const plain = pool.filter((row) => !isTool(row));
      const beside = plain.filter((row) =>
        [row.ord! - 1, row.ord! + 1].some((ord) => host.rows[ord - 1] && isTool(host.rows[ord - 1]!)));
      pool = beside.length > 0 ? beside : plain;
      if (pool.length === 0) return this.patch(sid, false);
    }
    const old = host.rows[ordOf(this.rng.pick(pool).id) - 1]!;
    const seq = ++host.seq;
    const tool = regroup || isTool(old);
    const row = { ...old, seq, content: `${tool ? "tool" : "row"} ${old.ord} r${seq}` };
    host.rows[old.ord! - 1] = row;
    this.log.push(`  ${regroup ? "regroup" : "patch"} ${row.id} seq ${seq}`);
    this.deliver(sid, row, "patch");
  }

  liveEvent(sid: string): void {
    if (this.rng.chance(0.6)) this.append(sid);
    else this.patch(sid, this.rng.chance(0.2));
  }

  publishReach(): void {
    const ord = ordOf(this.reader!.id);
    publishTranscriptReaderReach(this.current, {
      firstOrd: Math.max(1, ord - REACH_ORDS),
      lastOrd: ord + REACH_ORDS,
    });
  }

  /** Where the model's reader row sits in the current layout. */
  readerOffset(reader: Reader): number | undefined {
    const view = this.view!;
    const at = locate(view.items(), reader.id);
    if (!at) return undefined;
    view.virtualizer.getVirtualItemForOffset(0);
    const row = view.virtualizer.measurementsCache[at.index];
    return row ? row.start + at.entry * ENTRY_PX + reader.inRowPx : undefined;
  }

  async moveReader(row: Message): Promise<void> {
    this.reader = { id: row.id, inRowPx: this.rng.int(ENTRY_PX - 1) };
    const offset = this.readerOffset(this.reader);
    expect(offset, `reader target ${row.id} is laid out`).toBeDefined();
    this.view!.scrollTo(offset!);
    this.publishReach();
    await this.view!.settle();
    this.log.push(`  reader ${row.id}+${this.reader.inRowPx}`);
  }

  hydrate(sid: string): void {
    const page = hostQuery(this.hostOf(sid));
    // Rows that arrive while the bootstrap is in flight wait behind the hydration lock.
    for (let k = this.rng.int(3); k > 0; k--) this.liveEvent(sid);
    this.store.actions.installTranscriptBaseline(sid, page.messages, page.watermark, {
      hasMoreBefore: Boolean(page.before_cursor),
      hasMoreAfter: Boolean(page.after_cursor),
      bounds: spanFromWireBounds(page.ord_bounds),
    });
    completeChatHydrationAndReplay(this.store);
    replayForegroundBufferedMessages(this.store, sid);
    this.log.push(`  hydrate ${page.messages[0]?.ord}..${page.messages.at(-1)?.ord}`);
  }

  async start(): Promise<void> {
    this.log.push(`open ${this.current} (${this.host.rows.length} rows)`);
    this.store.actions.clearChatForSessionSwitch("*");
    this.store.actions.setCurrentSession(this.host.session);
    this.hydrate(this.current);
    this.view = mountView(this.store);
    await this.moveReader(this.rng.pick(this.presented()));
    this.check();
  }

  leave(): void {
    const view = this.view!;
    const position = transcriptReadingPosition(view.virtualizer, () => true, view.virtualizer.scrollOffset);
    expect(position, "reading position when leaving").not.toBeNull();
    this.saved.set(this.current, {
      transcript: structuredClone(unwrap(this.store.state.transcript)),
      reader: this.reader!,
      position: position!,
    });
    rememberSessionChatFromStore(this.store);
    publishTranscriptReaderReach(this.current, undefined);
    view.dispose();
    this.view = undefined;
  }

  /** A restored window equals the one left, and the saved reading position finds the same row. */
  async restoreReader(sid: string): Promise<void> {
    const saved = this.saved.get(sid);
    expect(saved, `restored ${sid} was left earlier`).toBeDefined();
    expect(unwrap(this.store.state.transcript), "restored window").toEqual(saved!.transcript);
    this.view = mountView(this.store);
    const offset = transcriptOffsetForPosition(this.view.virtualizer, saved!.position, this.view.resolve);
    expect(offset, `restored position ${JSON.stringify(saved!.position)}`).not.toBeNull();
    this.view.scrollTo(offset!);
    this.reader = saved!.reader;
    this.publishReach();
    await this.view.settle();
    this.checkReader();
  }

  async switchTo(sid: string): Promise<void> {
    const target = this.hostOf(sid);
    const scope = { projectId: target.session.project_id!, sessionId: sid };
    const sameProject = scope.projectId === this.project;
    this.leave();
    this.current = sid;
    this.project = scope.projectId;
    let restored = false;
    if (sameProject) {
      restored = restoreCachedSessionChat(this.store, scope);
      this.store.actions.beginSessionResumeSwitch(sid);
    } else {
      this.store.actions.clearChatForSessionSwitch("*");
      const cached = getSessionChatCache(scope);
      if (cached) {
        this.store.actions.restoreSessionChatSnapshot(cached);
        restored = true;
      }
    }
    this.log.push(`  ${sameProject ? "resume" : "cross-project"}${restored ? " restored" : ""}`);
    if (restored) await this.restoreReader(sid);
    else this.store.actions.setCurrentSession(target.session);
    this.hydrate(sid);
    if (!sameProject) {
      for (const host of this.hosts.values()) {
        if (host.session.project_id !== this.project) continue;
        for (const event of host.missed.splice(0)) applyMessageEvent(this.store, event);
      }
    }
    if (this.view) {
      await this.view.settle();
    } else {
      this.view = mountView(this.store);
      await this.moveReader(this.rng.pick(this.presented()));
    }
  }

  pickOp(): Op {
    const total = OP_WEIGHTS.reduce((sum, [, w]) => sum + w, 0);
    let roll = this.rng.int(total);
    for (const [op, weight] of OP_WEIGHTS) {
      if (roll < weight) return op;
      roll -= weight;
    }
    return "append";
  }

  async step(index: number): Promise<void> {
    const tw = this.store.state.transcript;
    let op = this.pickOp();
    if (op === "page-older" && !tw.hasMoreBefore) op = "append";
    if (op === "page-newer" && !tw.hasTailGap) op = "page-older";
    if (op === "page-older" && !tw.hasMoreBefore) op = "append";
    this.log.push(`${index} ${op}`);
    const presented = this.presented();
    switch (op) {
      case "append":
        this.append(this.current);
        break;
      case "patch":
      case "regroup":
        this.patch(this.current, op === "regroup");
        break;
      case "background": {
        const sid = this.rng.pick([...this.hosts.keys()].filter((id) => id !== this.current));
        this.log.push(`  in ${sid}`);
        this.liveEvent(sid);
        break;
      }
      case "page-older":
      case "page-newer": {
        // The reader nears the edge it pages from.
        const near = this.rng.int(Math.min(EDGE_ROWS, presented.length));
        await this.moveReader(presented[op === "page-older" ? near : presented.length - 1 - near]!);
        await loadTranscriptPage(this.client, this.store, this.current, op === "page-older" ? "older" : "newer");
        break;
      }
      case "evict":
        this.store.actions.trimTranscriptPages();
        break;
      case "resize": {
        const items = this.view!.items();
        const anchor = locate(items, this.reader!.id)?.index;
        const sizes: { index: number; size: number }[] = [];
        for (let k = 1 + this.rng.int(4); k > 0; k--) {
          const index = this.rng.int(items.length);
          if (index === anchor || items[index]!.kind !== "user" || sizes.some((s) => s.index === index)) continue;
          sizes.push({ index, size: ENTRY_PX + this.rng.int(200) });
        }
        this.log.push(`  sizes ${JSON.stringify(sizes)}`);
        this.view!.virtualizer.resizeItems(sizes);
        break;
      }
      case "scroll":
        await this.moveReader(this.rng.pick(presented));
        break;
      case "latest":
        this.store.actions.resetOlderTranscriptPages();
        await this.view!.settle();
        {
          const rows = this.presented();
          await this.moveReader(rows[rows.length - 1 - this.rng.int(Math.min(EDGE_ROWS, rows.length))]!);
        }
        break;
      case "switch": {
        const sid = this.rng.pick([...this.hosts.keys()].filter((id) => id !== this.current));
        this.log.push(`  to ${sid}`);
        await this.switchTo(sid);
        break;
      }
      case "restore": {
        const sid = this.current;
        this.leave();
        const scope = { projectId: this.project, sessionId: sid };
        expect(restoreCachedSessionChat(this.store, scope), "cached snapshot restores").toBe(true);
        await this.restoreReader(sid);
        break;
      }
    }
    await this.view!.settle();
    this.check();
    if (op === "evict") this.checkTrimmed();
  }

  check(): void {
    this.checkWindow();
    this.checkReader();
  }

  checkWindow(): void {
    const rows = this.host.rows;
    const newest = rows.length;
    const tw = unwrap(this.store.state.transcript) as TranscriptWindow;
    const messages = unwrap(this.store.state.messages) as Message[];
    expect(this.store.state.transcriptSessionId, "store names the session").toBe(this.current);
    const ids = new Set<string>();
    messages.forEach((row, i) => {
      expect(ids.has(row.id), `duplicate row ${row.id}`).toBe(false);
      ids.add(row.id);
      const want = rows[(row.ord ?? 0) - 1];
      if (row.id !== want?.id || row.seq !== want.seq || row.content !== want.content) {
        expect(row, `row ${row.id} is the host's newest revision`).toEqual(want);
      }
      if (i > 0) expect(row.ord!, `row ${row.id} follows ${messages[i - 1]!.id}`).toBeGreaterThan(messages[i - 1]!.ord!);
    });
    expect(tw.tail.at(-1)?.id, "live tail holds the newest row").toBe(rows.at(-1)!.id);
    expectContiguous(tw.tail, "live tail");
    const presented = this.presented();
    expect(presented.length, "presented rows").toBeGreaterThan(0);
    expectContiguous(presented, "presented window");
    const first = presented[0]!.ord!;
    const last = presented.at(-1)!.ord!;
    expect(tw.hasMoreBefore, `hasMoreBefore with presented ${first}..${last} of ${newest}`).toBe(first > 1);
    expect(tw.hasTailGap || tw.hasMoreAfter, `newer edge with presented ${first}..${last} of ${newest}`).toBe(last < newest);
    if (!tw.hasTailGap) expect(presented.length, "without a tail gap every resident row is presented").toBe(messages.length);
    expect(unloadedTranscriptOrds(tw), "unloaded ordinals").toEqual({ before: first - 1, after: newest - last });
    for (const [key, page] of Object.entries(tw.pages)) {
      expect(key, "page key").toBe(`${page[0]?.ord}:${page.at(-1)?.ord}`);
    }
    expect(residentPageOrds(tw.pages), "resident older pages").toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_ORD_HARD_CAP);
  }

  checkReader(): void {
    const reader = this.reader!;
    const expected = this.readerOffset(reader);
    expect(expected, `reader row ${reader.id} is presented`).toBeDefined();
    const actual = this.view!.virtualizer.scrollOffset ?? Number.NaN;
    expect(Math.abs(actual - expected!), `reader at ${actual}, ${reader.id}+${reader.inRowPx} at ${expected}`).toBeLessThanOrEqual(1);
  }

  /** Trimming leaves only the reader's pages above the budget. */
  checkTrimmed(): void {
    const ord = ordOf(this.reader!.id);
    const pages = Object.values(unwrap(this.store.state.transcript).pages);
    const touching = pages.some((page) =>
      page[0]!.ord! <= ord + REACH_ORDS && page.at(-1)!.ord! >= ord - REACH_ORDS);
    if (!touching) expect(residentPageOrds(this.store.state.transcript.pages), "trimmed pages").toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_ORD_BUDGET);
  }

  describeWindow(): string {
    const tw = unwrap(this.store.state.transcript);
    const span = (rows: readonly Message[]) => (rows.length ? `${rows[0]!.id}..${rows.at(-1)!.ord}` : "empty");
    return `session ${this.store.state.transcriptSessionId} tail ${span(tw.tail)} pages [${Object.keys(tw.pages).join(" ")}]` +
      ` gap ${tw.hasTailGap} before ${tw.hasMoreBefore} after ${tw.hasMoreAfter} host ${this.host.rows.length}`;
  }

  dispose(): void {
    this.view?.dispose();
    for (const sid of this.hosts.keys()) publishTranscriptReaderReach(sid, undefined);
  }
}

function expectContiguous(rows: readonly Message[], what: string): void {
  for (let i = 1; i < rows.length; i++) {
    expect(rows[i]!.ord, `${what} continues after ${rows[i - 1]!.id}`).toBe(rows[i - 1]!.ord! + 1);
  }
}

async function runSeed(seed: number): Promise<void> {
  const run = new ModelRun(seed);
  try {
    await run.start();
    for (let index = 0; index < STEPS; index++) await run.step(index);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    const at = (err instanceof Error ? err.stack ?? "" : "")
      .split("\n").filter((line) => line.includes("transcript-model.property")).slice(0, 4).join("\n");
    throw new Error(
      `seed ${seed} failed: ${message}\n` +
        `replay: -t "for seed ${seed}$" or DEN_TRANSCRIPT_MODEL_SEED=${seed}\n` +
        `window: ${run.describeWindow()}\n${at}\nsteps:\n${run.log.join("\n")}`,
      { cause: err },
    );
  } finally {
    run.dispose();
  }
}

afterEach(() => {
  clearSessionChatCacheForTests();
});

describe("transcript store model", () => {
  it.each(seedList())("keeps the window, tail and reader invariants for seed %i", async (seed) => {
    await runSeed(seed);
  });
});
