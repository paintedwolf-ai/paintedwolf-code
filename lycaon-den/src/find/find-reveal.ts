/** Collapsible hosts that Find can expand on activate and restore on leave. */

export type FindRevealHost = {
  id: string;
  /** This data provider supplies the corpus for its virtualized descendants. */
  ownsCorpus?: boolean;
  remoteMatches?: () => readonly { from: number; to: number }[];
  revealRemoteMatch?: (index: number) => void;
  /** Element containing the collapsible region. */
  hostEl: () => HTMLElement | null;
  isCollapsed: () => boolean;
  /** Expand for Find and return its restore action. */
  revealForFind: () => () => void;
  /** Searchable text for an unmounted body. */
  collapsedCorpus?: () => string;
  /** Last visible node before that unmounted corpus, used for document order. */
  collapsedCorpusAnchor?: () => Node | null | undefined;
};

const hosts = new Map<string, FindRevealHost>();

export function registerFindRevealHost(host: FindRevealHost): () => void {
  hosts.set(host.id, host);
  return () => {
    // Ignore cleanup after a host replacement.
    if (hosts.get(host.id) === host) hosts.delete(host.id);
  };
}

export function getFindRevealHost(id: string): FindRevealHost | null {
  return hosts.get(id) ?? null;
}

export function listFindRevealHosts(): FindRevealHost[] {
  return [...hosts.values()];
}

/** Return collapsed ancestors from outermost to innermost. */
export function collapsedRevealHostsContaining(node: Node | null): FindRevealHost[] {
  const chain: FindRevealHost[] = [];
  let el: Element | null =
    node instanceof Element ? node : (node?.parentElement ?? null);
  while (el) {
    for (const host of hosts.values()) {
      if (host.hostEl() === el && host.isCollapsed()) {
        chain.push(host);
        break;
      }
    }
    el = el.parentElement;
  }
  return chain.reverse();
}

/** Reset registry state for tests. */
export function resetFindRevealHostsForTests(): void {
  hosts.clear();
}
