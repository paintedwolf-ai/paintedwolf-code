import type { BackendConnection } from "../platform/connection/backend.ts";
import { clientIdentity, prepareClientIdentity } from "../platform/connection/client-identity.ts";
import { isRecord } from "../utils/type-guards.ts";
import type { EventEnvelope } from "./types.ts";
import { isKnownEventTopic } from "./event-topics.generated.ts";
import { lycaonFetch, requireSuccessfulResponse } from "./http.ts";

export type ParsedSSEMessage = {
  data?: string;
  comment?: string;
};

/** Three missed host heartbeat intervals also bound a stalled stream open. */
export const SSE_IDLE_TIMEOUT_MS = 60_000;

/** Split an SSE byte buffer into complete events. */
export function parseSSEBuffer(buffer: string): {
  messages: ParsedSSEMessage[];
  rest: string;
} {
  const messages: ParsedSSEMessage[] = [];
  const parts = buffer.split("\n\n");
  const rest = parts.pop() ?? "";
  for (const block of parts) {
    if (!block.trim()) continue;
    let data: string | undefined;
    let comment: string | undefined;
    for (const line of block.split("\n")) {
      if (line.startsWith("data:")) {
        data = line.slice(5).trimStart();
      } else if (line.startsWith(":")) {
        comment = line.slice(1).trimStart();
      }
    }
    messages.push({ data, comment });
  }
  return { messages, rest };
}

export function buildEventsUrl(
  connection: BackendConnection,
  projectId: string,
  after = "",
): string {
  const base = connection.baseUrl.replace(/\/$/, "");
  return `${base}${eventStreamPath(projectId, after)}`;
}

function eventStreamPath(projectId: string, after = ""): string {
  const query = new URLSearchParams();
  if (projectId) query.set("project_id", projectId);
  if (after) query.set("after", after);
  // Stream lifetime controls ephemeral presence only.
  query.set("client_id", clientIdentity());
  return `/v1/events?${query.toString()}`;
}

export async function* readAuthenticatedSSE(
  connection: BackendConnection,
  projectId: string,
  signal: AbortSignal,
  after = "",
): AsyncGenerator<ParsedSSEMessage> {
  await prepareClientIdentity();
  const controller = new AbortController();
  const abort = () => controller.abort(signal.reason);
  signal.addEventListener("abort", abort, { once: true });
  if (signal.aborted) abort();
  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  const armDeadline = () => {
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      controller.abort(new Error("Event stream stopped sending heartbeats."));
    }, SSE_IDLE_TIMEOUT_MS);
  };
  armDeadline();
  try {
    const res = await lycaonFetch(connection, eventStreamPath(projectId, after), {
      headers: { Accept: "text/event-stream" },
      signal: controller.signal,
    });
    await requireSuccessfulResponse(res);
    if (!res.body) throw new Error("SSE response missing body");
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        armDeadline();
        buffer += decoder.decode(value, { stream: true });
        const parsed = parseSSEBuffer(buffer);
        buffer = parsed.rest;
        for (const msg of parsed.messages) yield msg;
      }
      if (buffer.trim()) {
        const parsed = parseSSEBuffer(`${buffer}\n\n`);
        for (const msg of parsed.messages) yield msg;
      }
    } finally {
      await reader.cancel().catch(() => undefined);
      reader.releaseLock();
    }
  } finally {
    clearTimeout(idleTimer);
    signal.removeEventListener("abort", abort);
    controller.abort();
  }
}

const EVENT_ENVELOPE_FIELDS = {
  v: true,
  event_id: true,
  entity_revision: true,
  cursor: true,
  topic: true,
  published_at: true,
  scope: true,
  data: true,
} satisfies Record<keyof EventEnvelope, true>;
const EVENT_ENVELOPE_KEYS = new Set(Object.keys(EVENT_ENVELOPE_FIELDS));

function isEventScope(value: unknown): boolean {
  if (!isRecord(value) || typeof value.kind !== "string") return false;
  const keys = Object.keys(value);
  switch (value.kind) {
    case "device":
      return keys.length === 1;
    case "project":
      return keys.length === 2 && typeof value.project_id === "string";
    case "session":
      return (
        keys.length === 3 &&
        typeof value.project_id === "string" &&
        typeof value.session_id === "string"
      );
    default:
      return false;
  }
}

function isEventEnvelope(value: unknown): value is EventEnvelope {
  if (!isRecord(value)) return false;
  if (Object.keys(value).some((key) => !EVENT_ENVELOPE_KEYS.has(key))) return false;
  if (
    value.v !== 1 ||
    typeof value.event_id !== "string" ||
    typeof value.cursor !== "string" ||
    typeof value.topic !== "string" ||
    !isKnownEventTopic(value.topic) ||
    typeof value.published_at !== "string" ||
    !isEventScope(value.scope) ||
    !isRecord(value.data)
  ) {
    return false;
  }
  return (
    value.entity_revision === undefined ||
    (typeof value.entity_revision === "number" &&
      Number.isInteger(value.entity_revision) &&
      value.entity_revision >= 0)
  );
}

export function parseEventEnvelope(data: string): EventEnvelope | null {
  try {
    const value = JSON.parse(data) as unknown;
    return isEventEnvelope(value) ? value : null;
  } catch {
    return null;
  }
}
