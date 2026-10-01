import { createEffect, createSignal, onCleanup, untrack } from "solid-js";
import { findController, refreshFindResults } from "../../../find/find-controller.ts";
import { registerFindRevealHost } from "../../../find/find-reveal.ts";
import type { ChatContentAccess } from "./chat-content-reader.ts";

/** Search exact retained content while the card body is absent. */
export function bindChatContentFind(options: {
  access: () => ChatContentAccess | undefined;
  host: () => HTMLElement | null | undefined;
  collapsed: () => boolean;
  expand: () => () => void;
  reveal: (offset: number) => void;
}) {
  const [matches, setMatches] = createSignal<{ from: number; to: number }[]>([]);
  const id = `chat-content-find:${crypto.randomUUID()}`;
  createEffect(() => {
    const host = options.host(); if (!host) return;
    onCleanup(registerFindRevealHost({ id, hostEl: () => host,
      isCollapsed: options.collapsed, revealForFind: options.expand, remoteMatches: matches,
      revealRemoteMatch: index => { const match = matches()[index]; if (match) options.reveal(match.from); },
    }));
  });
  createEffect(() => {
    const access = options.access();
    const query = findController.query();
    const sensitive = findController.caseSensitive();
    const active = findController.isOpen();
    setMatches([]);
    if (!access || !query || !active) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void (async () => {
        const matches: { from: number; to: number }[] = [];
        for (let cursor: string | undefined = undefined; ;) {
          const page = await access.search(query, cursor, sensitive, abort.signal);
          if (abort.signal.aborted) return;
          matches.push(...page.matches.map(match => ({ from: match.offset, to: match.offset + match.length })));
          if (matches.length >= 10000 || !page.next_cursor) break;
          cursor = page.next_cursor;
        }
        setMatches(matches.slice(0, 10000));
        untrack(refreshFindResults);
      })().catch(() => { /* The reader exposes fetch errors and retry when opened. */ });
    }, 120);
    onCleanup(() => { clearTimeout(timer); abort.abort(); });
  });
}
