let pendingOpenWorklog = false;

export function requestOpenWorklog(): void {
  pendingOpenWorklog = true;
}

export function consumeOpenWorklog(): boolean {
  const value = pendingOpenWorklog;
  pendingOpenWorklog = false;
  return value;
}

export type OpenInSearchRequest = {
  originProjectId: string;
  query: string;
};

let pendingOpenSearch: OpenInSearchRequest | null = null;
let openInSearchSink: ((req: OpenInSearchRequest) => void) | null = null;

export function registerOpenInSearchSink(
  sink: (req: OpenInSearchRequest) => void,
): () => void {
  openInSearchSink = sink;
  if (pendingOpenSearch) {
    sink(pendingOpenSearch);
    pendingOpenSearch = null;
  }
  return () => {
    if (openInSearchSink === sink) openInSearchSink = null;
  };
}

export function openInSearch(originProjectId: string, query: string): void {
  const req: OpenInSearchRequest = {
    originProjectId: originProjectId.trim(),
    query: query.trim(),
  };
  if (!req.originProjectId || !req.query) return;
  if (openInSearchSink) openInSearchSink(req);
  else pendingOpenSearch = req;
}
