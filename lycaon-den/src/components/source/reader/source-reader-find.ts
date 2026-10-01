import { createEffect, createSignal, onCleanup, untrack } from "solid-js";
import { findController, refreshFindResults, isInFindScope } from "../../../find/find-controller.ts";
import { registerFindRevealHost } from "../../../find/find-reveal.ts";
import type { SourceReaderMatch } from "../../../api/types.ts";
import type { SourceReaderAccess } from "../../../api/source-reader.ts";

/** Search retained content without loading it into the transcript corpus. */
export function bindReaderFind(options: {
  access: () => SourceReaderAccess | undefined;
  host: () => HTMLElement | null | undefined;
  reveal: (row: number, match: SourceReaderMatch) => void | Promise<void>;
  collapsed?: () => boolean;
  expand?: () => () => void;
}) {
  const [matches, setMatches] = createSignal<SourceReaderMatch[]>([]);
  const id = `source-find:${crypto.randomUUID()}`;
  const [pending, setPending] = createSignal(false);
  const [error, setError] = createSignal<string>();
  let active = -1;
  createEffect(() => {
    const root = options.host(); if (!root) return;
    const stop = registerFindRevealHost({ id, hostEl: () => root, isCollapsed: options.collapsed ?? (() => false), revealForFind: options.expand ?? (() => () => {}),
      remoteMatches: matches,
      revealRemoteMatch: index => {
        if (active === index) return;
        active = index;
        const match = matches()[index];
        if (match) void Promise.resolve(options.reveal(match.row, match)).catch(cause => setError(String(cause)));
      },
    });
    onCleanup(stop);
  });
  createEffect(() => {
    const query = findController.query();
    const caseSensitive = findController.caseSensitive();
    const open = findController.isOpen();
    const access = options.access();
    void findController.activeViewId(); void findController.primaryViewId();
    const inScope = isInFindScope(options.host());
    active = -1; setMatches([]); setPending(false); setError(undefined);
    if (!open || !query || !access || !inScope) return;
    setPending(true);
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void (async () => {
        const found: SourceReaderMatch[] = [];
        for (let cursor: string | undefined; ;) {
          const page = await access.search(query, cursor, caseSensitive, abort.signal);
          if (abort.signal.aborted) return;
          found.push(...page.matches);
          if (!page.next_cursor || found.length >= 10000) break;
          if (page.next_cursor === cursor) throw new Error("The search range did not advance.");
          cursor = page.next_cursor;
        }
        setPending(false);
        setMatches(found.slice(0,10000));
        untrack(refreshFindResults);
      })().catch(cause => { if (!abort.signal.aborted) { setPending(false); setError(String(cause)); } });
    }, 120);
    onCleanup(() => { clearTimeout(timer); abort.abort(); });
  });
  return { pending, error };
}
