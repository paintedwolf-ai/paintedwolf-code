import { probeHealth, type BackendConnection } from "../platform/connection/backend.ts";
import {
  BackendTransportError,
  confirmBackendReachability,
  noteBackendReachable,
} from "../platform/connection/request-connectivity.ts";
import { isRecord } from "../utils/type-guards.ts";
import type { Error as ApiErrorResponse } from "./types.ts";
import { assertOperationRequest } from "./operations.generated.ts";

type LycaonApiErrorOptions = {
  retryAfterMs?: number;
  title?: ApiErrorResponse["title"];
  retryable?: ApiErrorResponse["retryable"];
  suggestedAction?: ApiErrorResponse["suggested_action"];
  actions?: ApiErrorResponse["actions"];
  tier?: ApiErrorResponse["tier"];
  scope?: ApiErrorResponse["scope"];
  resolution?: ApiErrorResponse["resolution"];
  details?: ApiErrorResponse["details"];
};

export class LycaonApiError extends Error {
  /** HTTP response status; absent for an error recorded inside an operation. */
  readonly status: number | undefined;
  readonly retryAfterMs?: number;
  readonly code: ApiErrorResponse["code"];
  readonly title?: ApiErrorResponse["title"];
  readonly retryable?: ApiErrorResponse["retryable"];
  readonly suggestedAction?: ApiErrorResponse["suggested_action"];
  readonly actions?: ApiErrorResponse["actions"];
  readonly tier?: ApiErrorResponse["tier"];
  /** Host-declared notice placement. */
  readonly scope?: ApiErrorResponse["scope"];
  readonly resolution?: ApiErrorResponse["resolution"];
  /** Structured error context from the wire `details` object. */
  readonly details?: ApiErrorResponse["details"];

  constructor(
    message: string,
    status: number | undefined,
    code: ApiErrorResponse["code"],
    opts?: LycaonApiErrorOptions,
  ) {
    super(message);
    this.name = "LycaonApiError";
    this.status = status;
    this.retryAfterMs = opts?.retryAfterMs;
    this.code = code;
    this.title = opts?.title;
    this.retryable = opts?.retryable;
    this.suggestedAction = opts?.suggestedAction;
    this.actions = opts?.actions;
    this.tier = opts?.tier;
    this.scope = opts?.scope;
    this.resolution = opts?.resolution;
    this.details = opts?.details;
  }
}

/** True when the host refused the request with one of `codes`. */
export function isApiErrorCode(
  error: unknown,
  codes: readonly ApiErrorResponse["code"][],
): error is LycaonApiError {
  return error instanceof LycaonApiError && codes.includes(error.code);
}

function requireToken(connection: BackendConnection): string {
  const token = connection.apiToken.trim();
  if (!token) {
    throw new Error(
      "Backend API token missing — call discoverBackend() before /v1 requests",
    );
  }
  return token;
}

function assertV1Path(path: string): void {
  if (!path.startsWith("/v1/")) {
    throw new Error(`API client only serves /v1 routes, got ${path}`);
  }
}

/** Builds "?key=val&…" from params, dropping absent and empty values; "" when none remain. */
export function formatQuery(
  entries: Record<string, string | number | boolean | undefined | null>,
): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(entries)) {
    if (value == null || value === "") continue;
    params.set(key, String(value));
  }
  const q = params.toString();
  return q ? `?${q}` : "";
}

/** The capability clients' JSON transport: `createLycaonClient` binds it to one connection. */
export type JsonRequester = <T>(path: string, init?: RequestInit) => Promise<T>;

export function jsonRequest(method: "POST" | "PUT" | "PATCH", body: unknown): RequestInit {
  return { method, body: JSON.stringify(body) };
}

export async function lycaonFetch(
  connection: BackendConnection,
  path: string,
  init: RequestInit = {},
): Promise<Response> {
  assertV1Path(path);
  assertOperationRequest(path, init.method ?? "GET");
  const token = requireToken(connection);
  const headers = new Headers(init.headers);
  if (!headers.has("Authorization")) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  if (init.body != null && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const url = `${connection.baseUrl.replace(/\/$/, "")}${path}`;
  const probe = (signal: AbortSignal) => probeHealth(connection.baseUrl, signal);
  const send = async (): Promise<Response> => {
    try {
      return await fetch(url, { ...init, headers });
    } catch (err) {
      if (init.signal?.aborted) throw init.signal.reason ?? err;
      const reachability = await confirmBackendReachability(probe);
      throw new BackendTransportError(err, reachability);
    }
  };
  let response = await send();
  // A 429 means the handler never ran, so any method can wait out Retry-After and replay.
  for (let waited = 0, refusals = 0; response.status === 429 && replayable(init.body); refusals++) {
    const delay = admissionDelay(parseRetryAfter(response.headers.get("Retry-After")), refusals);
    if (refusals >= ADMISSION_MAX_REPLAYS || waited + delay > ADMISSION_WAIT_BUDGET_MS) break;
    void response.body?.cancel().catch(() => undefined);
    await pause(delay, init.signal);
    waited += delay;
    response = await send();
  }
  // A gateway response needs a health probe; replaying could duplicate effects.
  if (response.status === 502 || response.status === 503 || response.status === 504) {
    const reachability = await confirmBackendReachability(probe);
    if (reachability === "unreachable") {
      throw new BackendTransportError(await apiErrorFromResponse(response), reachability);
    }
  } else {
    noteBackendReachable();
  }
  return response;
}

/** Budget for one request's admission waits. */
const ADMISSION_WAIT_BUDGET_MS = 30_000;
const ADMISSION_MAX_REPLAYS = 8;

/** Streams are one-shot. */
function replayable(body: RequestInit["body"]): boolean {
  return body == null || typeof body === "string" || body instanceof Blob || body instanceof ArrayBuffer
    || ArrayBuffer.isView(body) || body instanceof URLSearchParams || body instanceof FormData;
}

/** Retry-After, else exponential backoff; jitter spreads concurrent waiters. */
function admissionDelay(retryAfterMs: number | undefined, refusals: number): number {
  const base = retryAfterMs ?? Math.min(4_000, 500 * 2 ** refusals);
  return Math.round(base * (1 + Math.random() * 0.25));
}

function pause(ms: number, signal?: AbortSignal | null): Promise<void> {
  return new Promise((resolve, reject) => {
    const abortError = () => signal?.reason instanceof Error ? signal.reason : new DOMException("Request aborted", "AbortError");
    if (signal?.aborted) { reject(abortError()); return; }
    const done = () => { clearTimeout(timer); signal?.removeEventListener("abort", abort); };
    const abort = () => { done(); reject(abortError()); };
    const timer = setTimeout(() => { done(); resolve(); }, ms);
    signal?.addEventListener("abort", abort, { once: true });
  });
}

function parseRetryAfter(value: string | null): number | undefined {
  if (value === null) return undefined;
  const seconds = Number(value);
  const delay = Number.isFinite(seconds) ? seconds * 1000 : Date.parse(value) - Date.now();
  return Number.isFinite(delay) ? Math.max(0, delay) : undefined;
}

async function apiErrorFromResponse(res: Response): Promise<LycaonApiError> {
  let body: Partial<ApiErrorResponse> = {};
  try {
    const parsed = (await res.json()) as unknown;
    if (isRecord(parsed)) body = parsed as Partial<ApiErrorResponse>;
  } catch {
    // Non-JSON responses retain the HTTP status text.
  }
  return new LycaonApiError(body.message ?? res.statusText, res.status, body.code ?? "internal_error", {
    retryAfterMs: parseRetryAfter(res.headers.get("Retry-After")),
    title: body.title,
    retryable: body.retryable,
    suggestedAction: body.suggested_action,
    actions: body.actions,
    tier: body.tier,
    scope: body.scope,
    resolution: body.resolution,
    details:
      isRecord(body.details) ? body.details : undefined,
  });
}

/** Return successful responses and throw the canonical structured API error otherwise. */
export async function requireSuccessfulResponse(res: Response): Promise<Response> {
  if (!res.ok) {
    throw await apiErrorFromResponse(res);
  }
  return res;
}

/** Fetch a binary API response through the same error path as JSON requests. */
export async function lycaonBlob(
  connection: BackendConnection,
  path: string,
  init: RequestInit = {},
): Promise<Blob> {
  const res = await requireSuccessfulResponse(
    await lycaonFetch(connection, path, init),
  );
  return res.blob();
}

/** Read a server-provided attachment filename, with RFC 5987 support. */
function contentDispositionFilename(
  disposition: string | null,
  fallback: string,
): string {
  if (!disposition) return fallback;
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(disposition)?.[1];
  if (encoded) {
    try {
      return decodeURIComponent(encoded.trim());
    } catch {
      // Fall through to the plain filename form.
    }
  }
  const quoted = /filename="([^"]+)"/i.exec(disposition)?.[1];
  if (quoted) return quoted;
  return /filename=([^;]+)/i.exec(disposition)?.[1]?.trim() ?? fallback;
}

/** Fetch an attachment with canonical error handling and filename parsing. */
export async function lycaonDownload(
  connection: BackendConnection,
  path: string,
  fallbackFilename: string,
  init: RequestInit = {},
): Promise<{ blob: Blob; filename: string; headers: Headers }> {
  const res = await requireSuccessfulResponse(
    await lycaonFetch(connection, path, init),
  );
  return {
    blob: await res.blob(),
    filename: contentDispositionFilename(
      res.headers.get("Content-Disposition"),
      fallbackFilename,
    ),
    headers: res.headers,
  };
}

async function fetchJson<T>(
  connection: BackendConnection,
  path: string,
  init: RequestInit,
): Promise<T> {
  const res = await requireSuccessfulResponse(
    await lycaonFetch(connection, path, init),
  );
  if (res.status === 204) {
    return undefined as T;
  }
  try {
    return (await res.json()) as T;
  } catch (error) {
    // Body reads can fail after headers arrive; preserve cancellation reasons.
    if (init.signal?.aborted) throw init.signal.reason ?? error;
    if (error instanceof TypeError) {
      const reachability = await confirmBackendReachability((signal) => probeHealth(connection.baseUrl, signal));
      throw new BackendTransportError(error, reachability);
    }
    throw error;
  }
}

const inFlightJson = new Map<string, Promise<unknown>>();

/** Caller-provided abort signals require independent requests. */
function jsonShareKey(
  connection: BackendConnection,
  path: string,
  init: RequestInit,
): string | undefined {
  const method = (init.method ?? "GET").toUpperCase();
  if (method !== "GET" || init.signal) return undefined;
  return `${connection.baseUrl} ${path}`;
}

/** Shares concurrent GET parsing without caching settled responses. */
export async function lycaonJson<T>(
  connection: BackendConnection,
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const key = jsonShareKey(connection, path, init);
  if (key === undefined) return fetchJson<T>(connection, path, init);

  const pending = inFlightJson.get(key);
  if (pending) return structuredClone(await pending) as T;

  const request = fetchJson<unknown>(connection, path, init);
  inFlightJson.set(key, request);
  try {
    return (await request) as T;
  } finally {
    inFlightJson.delete(key);
  }
}
