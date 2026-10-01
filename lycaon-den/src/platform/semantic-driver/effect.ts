/**
 * What one drive step did to the page: attribute changes, nodes added and
 * removed, requests started, navigation, focus, and the aimed element
 * afterwards. An empty effect means the input arrived and changed nothing.
 */

import { type ElementBrief, type Json, describeElement } from "./core.ts";

/** One fetch or XMLHttpRequest the page started. */
export type RequestRecord = {
  seq: number;
  method: string;
  url: string;
  status?: number;
  failed?: boolean;
};

declare global {
  interface Window {
    __lycaonRequests?: { seq: number; log: RequestRecord[] };
  }
}

const MAX_REQUEST_LOG = 32;
const MAX_REQUEST_URL = 200;
const MAX_CHANGED_REPORT = 8;
const MAX_REQUEST_REPORT = 8;
const MAX_TRACKED_ELEMENTS = 200;
const MAX_ATTRIBUTE_VALUE = 160;

/** Records a request the page starts and returns its entry for completion. */
export function logRequest(method: string, url: string): RequestRecord {
  const requests = (window.__lycaonRequests ??= { seq: 0, log: [] });
  const entry: RequestRecord = {
    seq: ++requests.seq,
    method: method.toUpperCase().slice(0, 16),
    url: url.slice(0, MAX_REQUEST_URL),
  };
  requests.log.push(entry);
  if (requests.log.length > MAX_REQUEST_LOG) requests.log.shift();
  return entry;
}

function requestSeq(): number {
  return window.__lycaonRequests?.seq ?? 0;
}

function requestsSince(seq: number): RequestRecord[] {
  return (window.__lycaonRequests?.log ?? []).filter((r) => r.seq > seq);
}

type Recording = {
  id: number;
  observer: MutationObserver;
  /** First value each tracked attribute had before the step. */
  before: Map<Element, Map<string, string | null>>;
  records: number;
  added: number;
  removed: number;
  text: number;
  requestSeq: number;
  url: string;
  focus: Element | null;
  aimed: WeakRef<Element> | null;
};

let recording: Recording | null = null;
let nextRecordingID = 1;

/** Called when a step resolves its target, so the effect can report it afterwards. */
export function noteAimedElement(el: Element): void {
  if (recording) recording.aimed = new WeakRef(el);
}

function absorb(rec: Recording, mutations: MutationRecord[]): void {
  for (const m of mutations) {
    rec.records++;
    if (m.type === "attributes" && m.target instanceof Element && m.attributeName) {
      let attrs = rec.before.get(m.target);
      if (!attrs) {
        if (rec.before.size >= MAX_TRACKED_ELEMENTS) continue;
        attrs = new Map();
        rec.before.set(m.target, attrs);
      }
      if (!attrs.has(m.attributeName)) attrs.set(m.attributeName, m.oldValue);
    } else if (m.type === "childList") {
      for (const node of m.addedNodes) if (node instanceof Element) rec.added++;
      for (const node of m.removedNodes) if (node instanceof Element) rec.removed++;
      if ([...m.addedNodes, ...m.removedNodes].some((n) => n.nodeType === Node.TEXT_NODE)) rec.text++;
    } else if (m.type === "characterData") {
      rec.text++;
    }
  }
}

/** Starts recording one step's effect; a new call discards any unfinished one. */
export function beginEffect(): Json {
  recording?.observer.disconnect();
  const rec: Recording = {
    id: nextRecordingID++,
    observer: new MutationObserver((mutations) => absorb(rec, mutations)),
    before: new Map(),
    records: 0,
    added: 0,
    removed: 0,
    text: 0,
    requestSeq: requestSeq(),
    url: location.href,
    focus: document.activeElement,
    aimed: null,
  };
  rec.observer.observe(document.documentElement, {
    attributes: true,
    attributeOldValue: true,
    childList: true,
    subtree: true,
    characterData: true,
  });
  recording = rec;
  return { ok: true, id: rec.id };
}

function bounded(value: string | null): string | null {
  return value === null ? null : value.slice(0, MAX_ATTRIBUTE_VALUE);
}

/** State-bearing attributes first; inline style last, since animation rewrites it constantly. */
function attributeRank(name: string): number {
  if (name === "class" || name.startsWith("aria-") || name === "value" || name === "checked" || name === "selected") return 0;
  if (name.startsWith("data-")) return 1;
  if (name === "style") return 3;
  return 2;
}

function changedAttributes(rec: Recording): { changes: Json[]; total: number } {
  const changes: { rank: number; entry: Json }[] = [];
  for (const [el, attrs] of rec.before) {
    for (const [attribute, before] of attrs) {
      const after = el.getAttribute(attribute);
      if (after === before) continue;
      changes.push({
        rank: attributeRank(attribute),
        entry: {
          element: el.isConnected ? describeElement(el) : { ...describeElement(el), detached: true },
          attribute,
          before: bounded(before),
          after: bounded(after),
        },
      });
    }
  }
  changes.sort((a, b) => a.rank - b.rank);
  return { changes: changes.slice(0, MAX_CHANGED_REPORT).map((c) => c.entry), total: changes.length };
}

function aimedAfter(rec: Recording): (ElementBrief & { detached?: boolean }) | undefined {
  const el = rec.aimed?.deref();
  if (!el) return undefined;
  return el.isConnected ? describeElement(el) : { ...describeElement(el), detached: true };
}

/** Frames stall in a hidden page, so the wait for one is bounded. */
const FRAME_WAIT_MS = 50;

const nextFrame = () =>
  new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, FRAME_WAIT_MS);
    if (typeof requestAnimationFrame === "function") {
      requestAnimationFrame(() => {
        clearTimeout(timer);
        resolve();
      });
    }
  });

/**
 * Ends the recording `id` started. A recording that is gone means the step
 * replaced the document, and the report says so.
 */
export async function endEffect(opts: { id?: number } = {}): Promise<Json> {
  const rec = recording;
  if (!rec || rec.id !== opts.id) {
    return { ok: true, document_replaced: true, url: location.href };
  }
  // Rendering scheduled by the step lands before the next frame.
  await nextFrame();
  absorb(rec, rec.observer.takeRecords());
  rec.observer.disconnect();
  recording = null;

  const { changes, total } = changedAttributes(rec);
  const requests = requestsSince(rec.requestSeq);
  const out: Json = {
    ok: true,
    dom_changes: rec.records,
    changed: changes.length ? changes : undefined,
    changed_total: total > changes.length ? total : undefined,
    nodes_added: rec.added || undefined,
    nodes_removed: rec.removed || undefined,
    text_changes: rec.text || undefined,
    requests: requests.length ? requests.slice(0, MAX_REQUEST_REPORT) : undefined,
    requests_total: requests.length > MAX_REQUEST_REPORT ? requests.length : undefined,
    target_after: aimedAfter(rec),
  };
  if (location.href !== rec.url) out.url = { before: rec.url.slice(0, MAX_REQUEST_URL), after: location.href.slice(0, MAX_REQUEST_URL) };
  const focus = document.activeElement;
  if (focus !== rec.focus && focus && focus !== document.body) out.focus = describeElement(focus);
  return out;
}
