import { afterEach, expect, it, vi } from "vitest";
import { readAuthenticatedSSE, SSE_IDLE_TIMEOUT_MS } from "./events-sse.ts";

const connection = { baseUrl: "http://127.0.0.1:8787", apiToken: "fixture" };

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function streamFixture() {
  let body!: ReadableStreamDefaultController<Uint8Array>;
  let signal!: AbortSignal;
  let markOpened!: () => void;
  const opened = new Promise<void>((resolve) => { markOpened = resolve; });
  const cancelled = vi.fn();
  vi.stubGlobal("fetch", vi.fn((_url: string, init: RequestInit) => {
    signal = init.signal!;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        body = controller;
        if (signal.aborted) controller.error(signal.reason);
        else signal.addEventListener("abort", () => controller.error(signal.reason), { once: true });
        markOpened();
      },
      cancel: cancelled,
    });
    return Promise.resolve(new Response(stream));
  }));
  return {
    opened,
    send: (text: string) => body.enqueue(new TextEncoder().encode(text)),
    signal: () => signal,
    cancelled,
  };
}

it("aborts an open stream that silently stops delivering heartbeats", async () => {
  vi.useFakeTimers();
  const transport = streamFixture();
  const caller = new AbortController();
  const stream = readAuthenticatedSSE(connection, "p1", caller.signal, "cursor");
  const first = stream.next();
  await transport.opened;
  transport.send(": connected\n\n");
  expect((await first).value).toEqual({ comment: "connected", data: undefined });

  const failed = expect(stream.next()).rejects.toThrow("stopped sending heartbeats");
  await vi.advanceTimersByTimeAsync(SSE_IDLE_TIMEOUT_MS);
  await failed;
  expect(transport.signal().aborted).toBe(true);
  expect(caller.signal.aborted).toBe(false);
  expect(vi.getTimerCount()).toBe(0);
});

it("renews the deadline on live stream traffic and cleans up on close", async () => {
  vi.useFakeTimers();
  const transport = streamFixture();
  const stream = readAuthenticatedSSE(connection, "p1", new AbortController().signal);
  let next = stream.next();
  await transport.opened;
  transport.send(": connected\n\n");
  await next;

  for (let i = 0; i < 4; i++) {
    next = stream.next();
    await vi.advanceTimersByTimeAsync(20_000);
    transport.send(": ping\n\n");
    expect((await next).value?.comment).toBe("ping");
    expect(transport.signal().aborted).toBe(false);
  }
  await stream.return(undefined);
  expect(transport.cancelled).toHaveBeenCalledOnce();
  expect(transport.signal().aborted).toBe(true);
  expect(vi.getTimerCount()).toBe(0);
});

it("bounds a fetch that never returns response headers", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("fetch", vi.fn((_url: string, init: RequestInit) =>
    new Promise((_resolve, reject) => {
      init.signal!.addEventListener("abort", () => {
        const reason: unknown = init.signal!.reason;
        reject(reason instanceof Error ? reason : new Error("Request aborted"));
      }, { once: true });
    })));
  const stream = readAuthenticatedSSE(connection, "", new AbortController().signal);
  const failed = expect(stream.next()).rejects.toThrow("stopped sending heartbeats");

  await vi.advanceTimersByTimeAsync(SSE_IDLE_TIMEOUT_MS);
  await failed;
  expect(fetch).toHaveBeenCalledOnce();
  expect(vi.getTimerCount()).toBe(0);
});

it("propagates caller cancellation without waiting for the idle deadline", async () => {
  vi.useFakeTimers();
  streamFixture();
  const caller = new AbortController();
  const stream = readAuthenticatedSSE(connection, "", caller.signal);
  const reason = new Error("Scope closed");
  const failed = expect(stream.next()).rejects.toBe(reason);
  caller.abort(reason);

  await failed;
  expect(vi.getTimerCount()).toBe(0);
});
