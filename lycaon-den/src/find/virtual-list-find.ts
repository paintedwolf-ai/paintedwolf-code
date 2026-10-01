import { createEffect, createSignal, onCleanup, untrack } from "solid-js";
import { findController, refreshFindResults } from "./find-controller.ts";
import { findLiteralOffsets } from "./find-match.ts";
import { registerFindRevealHost } from "./find-reveal.ts";

export type VirtualListMatch = { from: number; to: number; field?: string; toolCallId?: string };

/** Find uses the virtual list data, so scrolling never changes its match count. */
export function bindVirtualListFind<T>(options: {
  items: () => readonly T[];
  host: () => HTMLElement | undefined;
  enabled: () => boolean;
  ownsCorpus?: boolean;
  corpus?: (item: T) => string;
  search?: (item: T, query: string, sensitive: boolean, signal: AbortSignal) => Promise<VirtualListMatch[]>;
  reveal: (index: number, match: VirtualListMatch) => void;
}) {
  const [matches, setMatches] = createSignal<(VirtualListMatch & { index: number })[]>([]);
  let active = -1;
  const id = `virtual-list-find:${crypto.randomUUID()}`;
  createEffect(() => {
    const host = options.host(); if (!host || !options.enabled()) return;
    onCleanup(registerFindRevealHost({ id, ownsCorpus: options.ownsCorpus ?? true, hostEl: () => host,
      isCollapsed: () => false, revealForFind: () => () => {}, remoteMatches: matches,
      revealRemoteMatch: index => {
        if (active === index) return;
        active = index;
        const match = matches()[index]; if (match) options.reveal(match.index, match);
      },
    }));
  });
  createEffect(() => {
    const query = findController.query(); const sensitive = findController.caseSensitive();
    const isOpen = findController.isOpen(); const items = options.items(); const enabled = options.enabled();
    active = -1; setMatches([]);
    if (!query || !isOpen || !enabled) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void (async () => {
        const found: (VirtualListMatch & { index: number })[] = [];
        for (let index = 0; index < items.length && found.length < 10000; index++) {
          if (abort.signal.aborted) return;
          const item = items[index]!;
          const matches = options.search ? await options.search(item, query, sensitive, abort.signal) :
            findLiteralOffsets(options.corpus?.(item) ?? "", query, sensitive, 10000 - found.length).map(match => ({ from: match.start, to: match.start + match.length }));
          found.push(...matches.slice(0, 10000 - found.length).map(match => ({ ...match, index })));
        }
        if (abort.signal.aborted) return;
        setMatches(found); untrack(refreshFindResults);
      })().catch(() => { /* A content read exposes its own error when opened. */ });
    }, 120);
    onCleanup(() => { clearTimeout(timer); abort.abort(); });
  });
}
