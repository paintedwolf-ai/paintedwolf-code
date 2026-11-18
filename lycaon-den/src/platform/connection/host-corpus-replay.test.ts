import { installConnectionFixtureCleanup, createFixtureApp, createLycaonClient, lastSubscribeHandlers, lastSubscribeOptions, loadModule } from "./app-connection-test-fixture.ts";

import { describe, expect, it, vi } from "vitest";

import type { EventSubscriptionOptions } from "../../api/events.ts";
import { readHostCorpus } from "../../test/host-corpus.ts";

installConnectionFixtureCleanup();

/** What the host's conformance sweep published, in delivery order. */
const hostEvents = readHostCorpus().events;

describe("host corpus replay", () => {
  it("parses every envelope the host published", async () => {
    const { parseEventEnvelope } = await vi.importActual<typeof import("../../api/events-sse.ts")>("../../api/events-sse.ts");
    const refused = hostEvents.filter(envelope => parseEventEnvelope(JSON.stringify(envelope)) === null);
    expect(refused.map(envelope => `${envelope.topic} ${envelope.event_id}`)).toEqual([]);
  });

  it("applies the host's event stream through the app's handlers without a failed apply", async () => {
    const mod = await loadModule();
    const appStore = createFixtureApp();
    createLycaonClient.mockReturnValue({ listProjects: vi.fn(async () => []) });
    await mod.connectAppBackend(appStore);
    const handlers = lastSubscribeHandlers!;
    const options = lastSubscribeOptions!;
    expect(handlers).toBeDefined();

    const { subscribeEvents } = await vi.importActual<typeof import("../../api/events.ts")>("../../api/events.ts");
    const errors: unknown[] = [];
    let delivered = 0;
    async function* replay(_c: unknown, _p: string, signal: AbortSignal) {
      yield { comment: "connected" };
      for (const envelope of hostEvents) {
        delivered += 1;
        yield { data: JSON.stringify(envelope) };
      }
      await new Promise<void>(resolve => signal.addEventListener("abort", () => resolve(), { once: true }));
    }
    // The fixture records the app's options with a narrower type than the real subscription takes.
    const appOptions = options as EventSubscriptionOptions;
    const subscription = subscribeEvents({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok" }, "", handlers as never, {
      ...appOptions,
      appStore,
      storeActions: appStore.actions,
      onReconcile: async () => {},
      onError: err => { errors.push(err); },
      connect: replay,
    });
    await subscription.ready;
    await vi.waitFor(() => expect(delivered).toBe(hostEvents.length));
    await subscription.close();

    expect(errors).toEqual([]);
  });
});
