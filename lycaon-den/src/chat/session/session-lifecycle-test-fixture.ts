import { stubClient, type ClientStubs } from "../../test/client-fixture.ts";
import { vi } from "vitest";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { type RecentsStore } from "../../store/recents-store.ts";
import type { LycaonClient } from "../../api/client.ts";

export function testProjectsStore(seed: ReturnType<typeof wireProject>[] = []) {
  const store = createProjectsStore(() => null, {
    onProjectEvent: () => () => {},
  });
  if (seed.length > 0) store.load(seed);
  return store;
}

export function withBootstrap(
  client: ClientStubs & {
    getSession: (id: string) => Promise<unknown>;
    listSessionMessages: (id: string) => Promise<unknown>;
  },
): LycaonClient {
  return stubClient(Object.assign(client, {
    getSessionBootstrap: vi.fn(async (id: string) => {
      const [session, transcript] = await Promise.all([
        client.getSession(id),
        client.listSessionMessages(id),
      ]);
      return {
        event_cursor: "",
        revision: (transcript as { watermark?: number }).watermark ?? 0,
        session,
        transcript,
        progress: { steps: [], revision: 0 },
        turn_clock: { session_id: id, active_ms: 0, work_ms: 0, running: false },
        findings: { findings: [], revision: 0 },
        queue: { queue_items: [], hold: false, sending: false, revision: 0 },
        coordinator: {},
        background_outputs: [],
        previews: [],
        workers: [],
        checkpoints: [],
      };
    }),
  }));
}

export function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

/** Recents whose disk write never settles, as under the persist debounce. */
export function stalledRecents(): RecentsStore {
  const never = () => new Promise<void>(() => undefined);
  return {
    registerSession: vi.fn(never),
    recordSessionActivity: vi.fn(never),
  } as unknown as RecentsStore;
}
