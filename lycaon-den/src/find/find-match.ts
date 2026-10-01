import {
  collapsedRevealHostsContaining,
  listFindRevealHosts,
} from "./find-reveal.ts";
import { FIND_MATCH_COUNT_CAP } from "./find-provider.ts";

/** Literal in-view find matching and highlights. */

export const FIND_MARK_ATTR = "data-den-find-mark";
export const FIND_MARK_INDEX_ATTR = "data-den-find-index";
export const FIND_MARK_CLASS = "den-find-mark";
export const FIND_MARK_ACTIVE_CLASS = "den-find-mark--active";
export const FIND_OVERLAY_ATTR = "data-den-find-overlay";

/** Soft cap on overlay marks painted for inactive matches (active always paints). */
export const FIND_MAX_INACTIVE_PAINT = 200;

export type FindMatch = {
  /** Document-order index among matches for this query. */
  index: number;
  remoteIndex?: number;
  /** Start offset in the root corpus. */
  startOffset: number;
  length: number;
  /** Live range invalidated by DOM mutations. */
  range: Range;
  /** True when the match is inside a collapsed reveal host (or synthetic corpus). */
  collapsed?: boolean;
  /** Registered reveal host that controls this match when collapsed / synthetic. */
  revealHostId?: string;
  /** Internal DOM-segment ordering key; synthetic bodies sit after their anchor. */
  sortOrder?: number;
};

type TextSegment = {
  node: Text;
  /** Start offset of this node in the flattened corpus. */
  start: number;
  length: number;
};

function isSkippableElement(el: Element): boolean {
  const tag = el.tagName;
  return (
    tag === "SCRIPT" ||
    tag === "STYLE" ||
    tag === "NOSCRIPT" ||
    tag === "TEXTAREA" ||
    el.hasAttribute("data-den-find-remote") ||
    el.hasAttribute(FIND_OVERLAY_ATTR)
  );
}

function isHiddenFromFind(el: Element): boolean {
  if (el.hasAttribute("hidden") || el.getAttribute("aria-hidden") === "true") {
    return true;
  }
  // Mounted collapsed hosts remain searchable; unmounted bodies use synthetic text.
  if (collapsedRevealHostsContaining(el).length > 0) return false;
  const style = getComputedStyle(el);
  return (
    style.display === "none" ||
    style.visibility === "hidden" ||
    style.visibility === "collapse"
  );
}

/** Collect searchable text nodes in document order. */
function collectTextSegments(root: HTMLElement): TextSegment[] {
  const segments: TextSegment[] = [];
  const dataRoots = listFindRevealHosts().filter(host => host.ownsCorpus).map(host => host.hostEl()).filter((element): element is HTMLElement => !!element);
  let offset = 0;
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      if (dataRoots.some(element => element.contains(node))) return NodeFilter.FILTER_REJECT;
      const parent = node.parentElement;
      if (!parent) return NodeFilter.FILTER_REJECT;
      if (isSkippableElement(parent)) return NodeFilter.FILTER_REJECT;
      let el: Element | null = parent;
      while (el && el !== root) {
        if (isSkippableElement(el) || isHiddenFromFind(el)) {
          return NodeFilter.FILTER_REJECT;
        }
        el = el.parentElement;
      }
      return NodeFilter.FILTER_ACCEPT;
    },
  });
  let current = walker.nextNode();
  while (current) {
    const text = current as Text;
    const length = text.data.length;
    if (length > 0) {
      segments.push({ node: text, start: offset, length });
      offset += length;
    }
    current = walker.nextNode();
  }
  return segments;
}

function flattenCorpus(segments: TextSegment[]): string {
  let out = "";
  for (const seg of segments) out += seg.node.data;
  return out;
}

/** Find all non-overlapping literal substring matches in document order. */
export function findLiteralOffsets(
  haystack: string,
  query: string,
  caseSensitive: boolean,
  cap = FIND_MATCH_COUNT_CAP,
): { start: number; length: number }[] {
  if (!query || cap <= 0) return [];
  const out: { start: number; length: number }[] = [];
  if (!caseSensitive) {
    const escaped = query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const matcher = new RegExp(escaped, "giu");
    let match: RegExpExecArray | null;
    while (out.length < cap && (match = matcher.exec(haystack)) !== null) {
      out.push({ start: match.index, length: match[0].length });
    }
    return out;
  }
  let from = 0;
  while (out.length < cap && from <= haystack.length - query.length) {
    const idx = haystack.indexOf(query, from);
    if (idx < 0) break;
    out.push({ start: idx, length: query.length });
    from = idx + query.length;
  }
  return out;
}

function rangeForOffsetFrom(
  segments: TextSegment[],
  startOffset: number,
  length: number,
  fromSeg: number,
): { range: Range; nextSeg: number } | null {
  if (length <= 0 || segments.length === 0 || fromSeg >= segments.length) {
    return null;
  }
  const endOffset = startOffset + length;
  let segIdx = fromSeg;
  while (
    segIdx < segments.length &&
    segments[segIdx]!.start + segments[segIdx]!.length <= startOffset
  ) {
    segIdx++;
  }
  if (segIdx >= segments.length) return null;
  const startSeg = segments[segIdx]!;
  const startNode = startSeg.node;
  const startLocal = Math.max(0, startOffset - startSeg.start);

  let endSegIdx = segIdx;
  while (
    endSegIdx < segments.length &&
    segments[endSegIdx]!.start + segments[endSegIdx]!.length < endOffset
  ) {
    endSegIdx++;
  }
  if (endSegIdx >= segments.length) return null;
  const endSeg = segments[endSegIdx]!;
  const endLocal = Math.max(0, endOffset - endSeg.start);

  const range = document.createRange();
  range.setStart(startNode, startLocal);
  range.setEnd(endSeg.node, endLocal);
  return { range, nextSeg: segIdx };
}

/** Match the query in root text. */
export function matchInRoot(
  root: HTMLElement,
  query: string,
  caseSensitive: boolean,
  cap = FIND_MATCH_COUNT_CAP,
): FindMatch[] {
  if (!query) return [];
  if (
    root instanceof HTMLTextAreaElement ||
    (root instanceof HTMLInputElement &&
      (root.type === "text" || root.type === "search" || root.type === ""))
  ) {
    return annotateCollapsedMatches(
      matchInTextField(root, query, caseSensitive, cap),
    );
  }
  const segments = collectTextSegments(root);
  const corpus = flattenCorpus(segments);
  const offsets = findLiteralOffsets(corpus, query, caseSensitive, cap);
  const matches: FindMatch[] = [];
  let segCursor = 0;
  for (let i = 0; i < offsets.length; i++) {
    const off = offsets[i]!;
    const built = rangeForOffsetFrom(segments, off.start, off.length, segCursor);
    if (!built) continue;
    segCursor = built.nextSeg;
    matches.push({
      index: matches.length,
      startOffset: off.start,
      length: off.length,
      range: built.range,
      sortOrder: built.nextSeg,
    });
  }
  return mergeCollapsedCorpusMatches(
    root,
    query,
    caseSensitive,
    annotateCollapsedMatches(matches),
    cap,
  );
}

/** Mark collapsed matches with their innermost reveal host. */
function annotateCollapsedMatches(matches: FindMatch[]): FindMatch[] {
  return matches.map((m) => {
    const chain = collapsedRevealHostsContaining(m.range.startContainer);
    if (chain.length === 0) return m;
    const innermost = chain[chain.length - 1]!;
    return { ...m, collapsed: true, revealHostId: innermost.id };
  });
}

/** Merge hidden host matches in document order. */
function mergeCollapsedCorpusMatches(
  root: HTMLElement,
  query: string,
  caseSensitive: boolean,
  existing: FindMatch[],
  cap: number,
): FindMatch[] {
  if (!query || existing.length >= cap) {
    return existing.slice(0, cap);
  }
  const synthetic: FindMatch[] = [];
  const segments = collectTextSegments(root);
  for (const host of listFindRevealHosts()) {
    const remaining = cap - existing.length - synthetic.length;
    if (remaining <= 0) break;
    if (!host.isCollapsed() && !host.remoteMatches) continue;
    const corpusFn = host.collapsedCorpus;
    if (!corpusFn && !host.remoteMatches) continue;
    const el = host.hostEl();
    if (!el || !root.contains(el)) continue;
    if (listFindRevealHosts().some(parent => parent !== host && parent.ownsCorpus && parent.hostEl()?.contains(el))) continue;
    const corpus = corpusFn?.() ?? "";
    const offsets = host.remoteMatches ? host.remoteMatches().slice(0, remaining).map((match, index) => ({ start: index, length: match.to - match.from })) : findLiteralOffsets(corpus, query, caseSensitive, remaining);
    if (offsets.length === 0) continue;
    for (const off of offsets) {
      const range = document.createRange();
      const anchor = host.collapsedCorpusAnchor?.();
      try {
        if (anchor?.parentNode) {
          range.setStartAfter(anchor);
          range.setEndAfter(anchor);
        } else {
          range.selectNode(el);
        }
      } catch {
        range.selectNodeContents(el);
      }
      synthetic.push({
        index: 0,
        startOffset: off.start,
        length: off.length,
        range,
        collapsed: host.isCollapsed(),
        remoteIndex: host.remoteMatches ? off.start : undefined,
        revealHostId: host.id,
        sortOrder: collapsedCorpusSortOrder(segments, anchor ?? el),
      });
    }
  }
  if (synthetic.length === 0) return existing;
  const merged = [...existing, ...synthetic];
  merged.sort((a, b) => {
    const order = (a.sortOrder ?? 0) - (b.sortOrder ?? 0);
    if (order !== 0) return order;
    return a.startOffset - b.startOffset;
  });
  return merged.map((m, i) => ({ ...m, index: i }));
}

/** Place an unmounted corpus directly after its last visible anchor segment. */
function collapsedCorpusSortOrder(
  segments: TextSegment[],
  anchor: Node,
): number {
  for (let i = segments.length - 1; i >= 0; i--) {
    const text = segments[i]!.node;
    if (anchor === text || anchor.contains(text)) return i + 0.5;
  }
  // No visible anchor sorts before the root corpus.
  return -0.5;
}

function matchInTextField(
  field: HTMLTextAreaElement | HTMLInputElement,
  query: string,
  caseSensitive: boolean,
  cap: number,
): FindMatch[] {
  const offsets = findLiteralOffsets(field.value, query, caseSensitive, cap);
  return offsets.map((off, i) => {
    const range = document.createRange();
    range.selectNode(field);
    return {
      index: i,
      startOffset: off.start,
      length: off.length,
      range,
    };
  });
}

/** Remove overlay highlights without changing text nodes. */
export function clearFindHighlights(root: HTMLElement | null): void {
  if (!root) return;
  root.querySelectorAll(`[${FIND_OVERLAY_ATTR}]`).forEach((el) => el.remove());
  root.classList.remove("den-findable-root");
}

function rectIntersectsViewport(
  rect: DOMRect,
  viewport: { top: number; bottom: number; left: number; right: number },
): boolean {
  return (
    rect.bottom >= viewport.top &&
    rect.top <= viewport.bottom &&
    rect.right >= viewport.left &&
    rect.left <= viewport.right
  );
}

function clientRectsForMatch(match: FindMatch): DOMRect[] {
  let rects: ArrayLike<DOMRect> = [];
  try {
    rects = match.range.getClientRects();
  } catch {
    rects = [];
  }
  if (rects.length === 0) {
    try {
      const r = match.range.getBoundingClientRect();
      if (r.width || r.height) rects = [r];
    } catch {
      rects = [];
    }
  }
  const out: DOMRect[] = [];
  for (let i = 0; i < rects.length; i++) out.push(rects[i]!);
  return out;
}

function appendMark(
  fragment: DocumentFragment,
  matchIndex: number,
  activeIndex: number,
  rect: DOMRect | null,
  rootRect: DOMRect,
  root: HTMLElement,
): HTMLElement {
  const box = document.createElement("span");
  box.setAttribute(FIND_MARK_ATTR, "1");
  box.setAttribute(FIND_MARK_INDEX_ATTR, String(matchIndex));
  const isActive = matchIndex === activeIndex;
  box.className = isActive
    ? `${FIND_MARK_CLASS} ${FIND_MARK_ACTIVE_CLASS}`
    : FIND_MARK_CLASS;
  if (isActive) box.setAttribute("aria-current", "true");
  if (rect) {
    box.style.position = "absolute";
    box.style.left = `${rect.left - rootRect.left + root.scrollLeft}px`;
    box.style.top = `${rect.top - rootRect.top + root.scrollTop}px`;
    box.style.width = `${Math.max(rect.width, 0)}px`;
    box.style.height = `${Math.max(rect.height, 0)}px`;
    box.style.pointerEvents = "none";
  }
  fragment.appendChild(box);
  return box;
}

/** Paint the active match and capped visible inactive matches. */
export function applyFindHighlights(
  root: HTMLElement,
  matches: FindMatch[],
  activeIndex: number,
): HTMLElement | null {
  if (
    root instanceof HTMLTextAreaElement ||
    root instanceof HTMLInputElement
  ) {
    return null;
  }
  clearFindHighlights(root);
  if (matches.length === 0) return null;

  root.classList.add("den-findable-root");

  const overlay = document.createElement("div");
  overlay.setAttribute(FIND_OVERLAY_ATTR, "1");
  overlay.className = "den-find-overlay";
  overlay.setAttribute("aria-hidden", "true");

  const rootRect = root.getBoundingClientRect();
  const viewport = {
    top: rootRect.top,
    bottom: rootRect.bottom,
    left: rootRect.left,
    right: rootRect.right,
  };
  const fragment = document.createDocumentFragment();
  let activeEl: HTMLElement | null = null;
  let inactivePainted = 0;
  let painted = 0;

  for (let i = 0; i < matches.length; i++) {
    const match = matches[i]!;
    const isActive = i === activeIndex;
    if (!isActive && inactivePainted >= FIND_MAX_INACTIVE_PAINT) continue;

    const rects = clientRectsForMatch(match);
    // Zero-size ranges still receive an observable sentinel.
    if (rects.length === 0) {
      if (!isActive && inactivePainted >= FIND_MAX_INACTIVE_PAINT) continue;
      const box = appendMark(fragment, i, activeIndex, null, rootRect, root);
      if (isActive) activeEl = box;
      else inactivePainted++;
      painted++;
      continue;
    }

    let anyVisible = false;
    for (let j = 0; j < rects.length; j++) {
      if (rectIntersectsViewport(rects[j]!, viewport)) {
        anyVisible = true;
        break;
      }
    }
    if (!isActive && !anyVisible) continue;
    if (!isActive && inactivePainted >= FIND_MAX_INACTIVE_PAINT) continue;

    for (let j = 0; j < rects.length; j++) {
      const box = appendMark(
        fragment,
        i,
        activeIndex,
        rects[j]!,
        rootRect,
        root,
      );
      if (isActive) activeEl = box;
    }
    if (!isActive) inactivePainted++;
    painted++;
  }

  if (painted > 0) {
    overlay.appendChild(fragment);
    try {
      root.appendChild(overlay);
    } catch {
      /* The root may detach during painting. */
    }
  }
  return activeEl;
}

/** Update active styling without rebuilding the overlay. */
export function setActiveFindMark(
  root: HTMLElement,
  activeIndex: number,
): HTMLElement | null {
  let activeEl: HTMLElement | null = null;
  root.querySelectorAll(`[${FIND_MARK_ATTR}]`).forEach((node) => {
    const el = node as HTMLElement;
    const idx = Number(el.getAttribute(FIND_MARK_INDEX_ATTR));
    const isActive = idx === activeIndex;
    el.classList.toggle(FIND_MARK_ACTIVE_CLASS, isActive);
    if (isActive) {
      el.setAttribute("aria-current", "true");
      if (!activeEl) activeEl = el;
    } else {
      el.removeAttribute("aria-current");
    }
  });
  return activeEl;
}

/** Re-resolve matches after DOM mutation. */
export function refreshMatches(
  root: HTMLElement,
  query: string,
  caseSensitive: boolean,
  previousActiveIndex: number,
): { matches: FindMatch[]; activeIndex: number } {
  const matches = matchInRoot(root, query, caseSensitive);
  let activeIndex = previousActiveIndex;
  if (matches.length === 0) activeIndex = -1;
  else if (activeIndex < 0) activeIndex = 0;
  else if (activeIndex >= matches.length) activeIndex = matches.length - 1;
  try {
    applyFindHighlights(root, matches, activeIndex);
  } catch {
    /* The root may detach during refresh. */
  }
  return { matches, activeIndex };
}
