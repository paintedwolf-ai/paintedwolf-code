import { readFileSync } from "node:fs";

import type { EventEnvelope } from "../types.ts";

/** One request the host's conformance sweep sent and the answer it got. */
export interface CorpusExchange {
  operation: string;
  method: string;
  path: string;
  params?: Record<string, string>;
  status: number;
  body?: unknown;
}

/** What the host's conformance sweep observed it answer and publish. */
export interface HostCorpus {
  exchanges: CorpusExchange[];
  events: EventEnvelope[];
}

/** The corpus `TestAPIConformance` records; tests replay it into Den. */
export function readHostCorpus(): HostCorpus {
  return JSON.parse(readFileSync(new URL("./host-corpus.json", import.meta.url), "utf8")) as HostCorpus;
}
