import { beforeEach, describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { EventScope } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { loaded, unloaded } from "../../store/load-state.ts";
import { connectionSessionEvents } from "./connection-session-events.ts";

const { refreshFindings, refreshProgress, refreshQueue } = vi.hoisted(() => ({
  refreshFindings: vi.fn(() => Promise.resolve()),
  refreshProgress: vi.fn(() => Promise.resolve()),
  refreshQueue: vi.fn(() => Promise.resolve()),
}));
vi.mock("../../chat/actions/findings-actions.ts", () => ({ refreshFindings }));
vi.mock("../../chat/progress/progress-actions.ts", () => ({ refreshProgress }));
vi.mock("../../chat/actions/queue-actions.ts", () => ({ refreshQueue }));

const client = stubClient();

/** The typed stub throws on deep inspection, so calls match the client by identity. */
function expectCall(fn: { mock: { calls: unknown[][] } }, ...expected: unknown[]) {
  const label = (args: readonly unknown[]) => args.map((arg) => (arg === client ? "<client>" : arg));
  expect(fn.mock.calls.map(label)).toContainEqual(label(expected));
}
const foreground: EventScope = { kind: "session", project_id: "proj-1", session_id: "session-a" };
const background: EventScope = { kind: "session", project_id: "proj-1", session_id: "session-b" };

function store(revision: number, sessionId: string | undefined = "session-a"): AppStore {
  return {
    state: {
      currentSession: sessionId ? { id: sessionId } : undefined,
      findings: revision ? loaded({ revision }) : unloaded(),
      progress: revision ? { revision } : undefined,
      queueDraft: revision ? { revision } : undefined,
    },
  } as unknown as AppStore;
}

type Topic = "findings" | "progress" | "queue";
const refreshers = { findings: refreshFindings, progress: refreshProgress, queue: refreshQueue };

function deliver(
  appStore: AppStore,
  topic: Topic,
  revision: number,
  scope: EventScope,
  getClient: () => LycaonClient | null = () => client,
) {
  const handlers = connectionSessionEvents(appStore, getClient);
  const handler = handlers[topic] as (ev: { revision: number }, scope: EventScope) => void;
  handler({ revision }, scope);
}

beforeEach(() => vi.clearAllMocks());

describe.each<Topic>(["findings", "progress", "queue"])("connection %s events", (topic) => {
  const refresh = refreshers[topic];

  it("refreshes the foreground session to a newer revision", () => {
    const appStore = store(2);
    deliver(appStore, topic, 3, foreground);
    expectCall(refresh, appStore, client, "session-a", 3);
  });

  it("refreshes from an empty projection", () => {
    const appStore = store(0);
    deliver(appStore, topic, 1, foreground);
    expectCall(refresh, appStore, client, "session-a", 1);
  });

  it("ignores a revision the foreground projection already holds", () => {
    deliver(store(3), topic, 3, foreground);
    deliver(store(3), topic, 2, foreground);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("ignores background sessions and device scope", () => {
    deliver(store(1), topic, 5, background);
    deliver(store(1), topic, 5, { kind: "device" });
    expect(refresh).not.toHaveBeenCalled();
  });

  it("ignores device scope while no session is foreground", () => {
    deliver(store(0, undefined), topic, 5, { kind: "device" });
    expect(refresh).not.toHaveBeenCalled();
  });

  it("waits for a connected client", () => {
    deliver(store(1), topic, 5, foreground, () => null);
    expect(refresh).not.toHaveBeenCalled();
  });
});

describe("connection session event failures", () => {
  it("contains failed progress and queue refreshes", async () => {
    refreshProgress.mockRejectedValueOnce(new Error("gone"));
    refreshQueue.mockRejectedValueOnce(new Error("gone"));
    deliver(store(1), "progress", 2, foreground);
    deliver(store(1), "queue", 2, foreground);
    await Promise.resolve();
    expect(refreshProgress).toHaveBeenCalledTimes(1);
    expect(refreshQueue).toHaveBeenCalledTimes(1);
  });
});
