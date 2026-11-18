// @vitest-environment jsdom
import { stubClient } from "../../test/client-fixture.ts";

import { createRoot, createSignal } from "solid-js";
import { createStore } from "solid-js/store";
import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { bindPaintInterestLiveStreams } from "./paint-interest-live.ts";

const settleEffects = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("paint interest live streams", () => {
  it("suspends a hidden resident and resumes the current stream on activation", async () => {
    const signals: AbortSignal[] = [];
    const client = stubClient({ streamSession: async (_id: string, options?: { signal?: AbortSignal }) => {
      if (options?.signal) signals.push(options.signal);
      return new Response(new ReadableStream<Uint8Array>());
    } });
    const [active, setActive] = createSignal(false);
    const state = { currentSession: { id: "s", status: "busy" }, sessionActivity: {},
      messages: [{ id: "m", role: "assistant", status: "streaming", content: "" }], workerTranscripts: {} };
    const dispose = createRoot(dispose => {
      bindPaintInterestLiveStreams({ appStore: { state } as unknown as AppStore, client: () => client, sessionId: () => "s", active });
      return dispose;
    });
    await settleEffects();
    expect(signals).toHaveLength(0);
    setActive(true);
    await settleEffects();
    expect(signals).toHaveLength(1);
    setActive(false);
    await settleEffects();
    expect(signals[0]?.aborted).toBe(true);
    setActive(true);
    await settleEffects();
    expect(signals).toHaveLength(2);
    dispose();
    expect(signals[1]?.aborted).toBe(true);
  });
  it("keeps one subscription while live content replaces the same row", async () => {
    const [state, setState] = createStore({
      currentSession: { id: "session-1", status: "busy" },
      sessionActivity: {},
      messages: [
        {
          id: "message-1",
          role: "assistant",
          status: "streaming",
          content: "one",
        } as Message,
      ],
      workerTranscripts: {},
    });
    const requestSignals: AbortSignal[] = [];
    const client = stubClient({
      streamSession: async (
        _sessionId: string,
        opts?: { signal?: AbortSignal },
      ) => {
        if (opts?.signal) requestSignals.push(opts.signal);
        return new Response(new ReadableStream<Uint8Array>());
      },
    });

    let dispose: () => void = () => {};
    createRoot((rootDispose) => {
      dispose = rootDispose;
      bindPaintInterestLiveStreams({
        appStore: { state } as unknown as AppStore,
        client: () => client,
        sessionId: () => "session-1",
      });
    });
    await settleEffects();
    expect(requestSignals).toHaveLength(1);

    setState("messages", 0, {
      ...state.messages[0]!,
      content: "two",
    });
    await settleEffects();
    expect(requestSignals).toHaveLength(1);
    expect(requestSignals[0]?.aborted).toBe(false);

    setState("messages", 0, "status", "complete");
    await settleEffects();
    expect(requestSignals[0]?.aborted).toBe(true);
    dispose();
  });
  it("follows a shown worker without a parent session and stops when it is no longer shown", async () => {
    const requests: { sessionId: string; signal?: AbortSignal }[] = [];
    const client = stubClient({
      streamSession: async (sessionId: string, opts?: { signal?: AbortSignal }) => {
        requests.push({ sessionId, signal: opts?.signal });
        return new Response(new ReadableStream<Uint8Array>());
      },
    });
    const state = {
      currentSession: { id: "session-1", status: "busy" },
      sessionActivity: {},
      messages: [{ id: "message-1", role: "assistant", status: "streaming", content: "" }],
      workerTranscripts: {
        "worker-1": { rows: [{ id: "row-1", role: "assistant", status: "streaming", content: "" }] },
      },
    };
    const [shown, setShown] = createSignal(true);
    const dispose = createRoot((rootDispose) => {
      bindPaintInterestLiveStreams({
        appStore: { state } as unknown as AppStore,
        client: () => client,
        paintedWorker: () =>
          shown() ? { workerId: "worker-1", childSessionId: "child-1" } : null,
      });
      return rootDispose;
    });
    await settleEffects();
    expect(requests.map((request) => request.sessionId)).toEqual(["child-1"]);

    setShown(false);
    await settleEffects();
    expect(requests).toHaveLength(1);
    expect(requests[0]?.signal?.aborted).toBe(true);
    dispose();
  });
});
