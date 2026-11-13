import { createEffect, createSignal, onCleanup, untrack } from "solid-js";
import { isApiErrorCode } from "../../../api/http.ts";
import { findController, refreshFindResults } from "../../../find/find-controller.ts";
import { registerFindRevealHost } from "../../../find/find-reveal.ts";
import type { ChatContentAccess, ChatContentMatch } from "./chat-content-reader.ts";

const MAX_MATCHES = 10000;

/** Searches content on the host, so Find covers rows that are not loaded. */
export function bindChatContentFind(options: {
  access: () => ChatContentAccess | undefined;
  host: () => HTMLElement | null | undefined;
  collapsed: () => boolean;
  expand: () => () => void;
  reveal: (match: ChatContentMatch) => void;
  /** Receives a search the content kept changing under. */
  onError?: (cause: unknown) => void;
}) {
  const [matches, setMatches] = createSignal<readonly ChatContentMatch[]>([]);
  const id = `chat-content-find:${crypto.randomUUID()}`;
  createEffect(() => {
    const host = options.host(); if (!host) return;
    onCleanup(registerFindRevealHost({ id, hostEl: () => host,
      isCollapsed: options.collapsed, revealForFind: options.expand,
      remoteMatches: () => matches().map(match => ({ from: 0, to: match.length })),
      revealRemoteMatch: index => { const match = matches()[index]; if (match) options.reveal(match); },
    }));
  });
  // One refresh per query: a search the content changed under runs once more on the new revision.
  let refreshedFor: string | undefined;
  createEffect(() => {
    const access = options.access();
    const query = findController.query();
    const sensitive = findController.caseSensitive();
    const active = findController.isOpen();
    access?.revision();
    setMatches([]);
    if (!access || !query || !active) return;
    const search = `${query}\0${sensitive}`;
    if (refreshedFor !== undefined && refreshedFor !== search) refreshedFor = undefined;
    const abort = new AbortController();
    const collect = async () => {
      const found: ChatContentMatch[] = [];
      for (let cursor: string | undefined = undefined; ;) {
        const page = await access.find(query, cursor, sensitive, abort.signal);
        if (abort.signal.aborted) return undefined;
        found.push(...page.matches);
        if (found.length >= MAX_MATCHES || !page.next_cursor) return found;
        cursor = page.next_cursor;
      }
    };
    const timer = setTimeout(() => {
      void (async () => {
        // An engine restart expires cursors; the search starts over once.
        const found = await collect().catch(cause => {
          if (isApiErrorCode(cause, ["cursor_generation_expired"])) return collect();
          throw cause;
        });
        if (!found) return;
        setMatches(found.slice(0, MAX_MATCHES));
        untrack(refreshFindResults);
      })().catch(cause => {
        if (abort.signal.aborted || !isApiErrorCode(cause, ["chat_content_changed"])) return;
        if (refreshedFor === search) { options.onError?.(cause); return; }
        refreshedFor = search;
        void access.refresh(abort.signal).catch(() => { /* The reader reports read failures. */ });
      });
    }, 120);
    onCleanup(() => { clearTimeout(timer); abort.abort(); });
  });
}
