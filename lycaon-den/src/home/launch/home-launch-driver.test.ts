import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { HomeStart, PromptRequest } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { LycaonClient } from "../../api/client.ts";
import { configureLaunchDriver, driveLaunch, haltLaunch, wakeLaunch } from "./home-launch-driver.ts";
import {
  applyHostState,
  flushHomeLaunchWrites,
  homeLaunches,
  resetHomeLaunchesForTest,
  stageLaunchBytes,
  submitHomeLaunch,
  updateLaunch,
} from "./home-launches.ts";
import { memoryHomeOutbox } from "./home-outbox.ts";
import type { LaunchChip } from "./home-launch-model.ts";

const PROJECT = "11111111-1111-4111-8111-111111111111";
const SESSION = "22222222-2222-4222-8222-222222222222";

/** A host that binds a launch on its first read and admits only its current operation. */
class FakeHost {
  phase: HomeStart["phase"] = "reserved";
  revision = 0;
  reserved = false;
  operation = "";
  admitted = false;
  sessionStatus: HomeStart["session_status"] = "idle";
  prompts: PromptRequest[] = [];
  puts = 0;
  loseNextPromptReply = false;

  view(launchId: string): HomeStart {
    const bound = this.phase === "session_bound" || (this.reserved && this.phase !== "reserved");
    return {
      launch_id: launchId,
      phase: this.phase,
      revision: this.revision,
      ...(bound || (this.phase === "stopped" && this.reserved) ? { project_id: PROJECT, session_id: SESSION, session_status: this.sessionStatus } : {}),
      first_prompt: { operation_id: this.operation || launchId, admitted: this.admitted, ...(this.admitted ? { status: "queued" as const } : {}) },
    };
  }

  client(): LycaonClient {
    return stubClient({
      getHomeStartCapabilities: vi.fn(async () => ({ protocol_version: 1, store_id: "store-1", person_id: "person-1" })),
      reserveHomeStart: vi.fn(async (launchId: string) => {
        this.puts++;
        if (!this.reserved && this.phase !== "stopped") {
          this.reserved = true;
          this.operation ||= launchId;
          this.revision++;
        }
        return this.view(launchId);
      }),
      getHomeStart: vi.fn(async (launchId: string) => {
        if (this.reserved && this.phase === "reserved") {
          this.phase = "session_bound";
          this.revision++;
        }
        return this.view(launchId);
      }),
      stopHomeStart: vi.fn(async (launchId: string) => {
        if (!this.admitted) { this.phase = "stopped"; this.revision++; }
        return this.view(launchId);
      }),
      reissueHomeStartFirstPrompt: vi.fn(async (launchId: string, req: { operation_id: string; replacement_operation_id: string }) => {
        if (this.admitted) throw new LycaonApiError("admitted", 409, "home_start_admitted");
        if (req.operation_id !== this.operation) throw new LycaonApiError("stale", 409, "home_start_conflict");
        this.operation = req.replacement_operation_id;
        this.revision++;
        return this.view(launchId);
      }),
      sendPrompt: vi.fn(async (_sessionId: string, req: PromptRequest) => {
        if (req.operation_id !== this.operation || this.phase === "stopped") {
          throw new LycaonApiError("closed", 409, "home_start_closed");
        }
        if (!this.admitted) {
          this.prompts.push(req);
          this.admitted = true;
          this.revision++;
        }
        if (this.loseNextPromptReply) {
          this.loseNextPromptReply = false;
          throw new TypeError("Load failed");
        }
        return { status: "queued", operation_id: req.operation_id, message_id: req.operation_id, session_revision: 1 };
      }),
      uploadAttachment: vi.fn(async (_projectId: string, filename: string, mime: string) => ({
        blob_id: "b".repeat(64), filename, mime, kind: "text", bytes: 4,
      })),
    }) as LycaonClient;
  }
}

let host: FakeHost;
let client: LycaonClient | null;
let reconnect: () => void;
const handoffs: { launchId: string; sessionId: string; followUp?: string }[] = [];

beforeEach(() => {
  resetHomeLaunchesForTest(memoryHomeOutbox());
  host = new FakeHost();
  client = host.client();
  handoffs.length = 0;
  configureLaunchDriver({
    client: () => client,
    onConnectionChange: (listener) => { reconnect = listener; return () => undefined; },
    handoff: (launchId, chat, followUp) => handoffs.push({ launchId, sessionId: chat.sessionId, ...(followUp ? { followUp } : {}) }),
    sleep: async () => undefined,
  });
});

afterEach(() => {
  for (const record of homeLaunches.pending()) haltLaunch(record.launchId);
});

function launch(text: string, chips: LaunchChip[] = []): string {
  return submitHomeLaunch(
    { text, mode: "agent", chips, compositionId: crypto.randomUUID(), compositionRevision: 1 },
    () => undefined,
  );
}

describe("home launch driver", () => {
  it("reserves, waits for the bound chat, sends the exact idea once and hands off", async () => {
    const id = launch("Build a tic tac game with AI I can beat");
    driveLaunch(id);
    await vi.waitFor(() => expect(handoffs).toEqual([{ launchId: id, sessionId: SESSION }]));
    expect(host.puts).toBe(1);
    expect(host.prompts).toEqual([
      expect.objectContaining({ operation_id: id, text: "Build a tic tac game with AI I can beat", mode: "agent" }),
    ]);
  });

  it("recovers a lost prompt reply through the receipt instead of a second turn", async () => {
    host.loseNextPromptReply = true;
    const id = launch("Lost reply");
    driveLaunch(id);
    await vi.waitFor(() => expect(handoffs).toHaveLength(1));
    expect(host.prompts).toHaveLength(1);
  });

  it("checks admission before resending after an uncertain send", async () => {
    const id = launch("Resume me");
    host.reserved = true;
    host.phase = "session_bound";
    host.operation = id;
    host.admitted = true;
    host.revision = 3;
    host.prompts.push({ operation_id: id, text: "Resume me" });
    updateLaunch(id, (r) => ({
      ...r,
      sendAttempted: true,
      hostState: { ...host.view(id), first_prompt: { operation_id: id, admitted: false }, revision: 2 },
      firstPrompt: { text: "Resume me", attachmentLabels: [], attachments: [], references: [] },
    }));
    driveLaunch(id);
    await vi.waitFor(() => expect(handoffs).toHaveLength(1));
    expect(host.prompts).toHaveLength(1);
    expect(vi.mocked(client!.sendPrompt)).not.toHaveBeenCalled();
  });

  it("waits offline with the idea kept, then continues on reconnect", async () => {
    client = null;
    const id = launch("Offline idea");
    driveLaunch(id);
    await vi.waitFor(() => expect(homeLaunches.runtime(id)?.blocker?.code).toBe("engine_offline"));
    expect(homeLaunches.record(id)?.text).toBe("Offline idea");
    client = host.client();
    reconnect();
    await vi.waitFor(() => expect(handoffs).toHaveLength(1));
  });

  it("a Stop before the PUT reserves nothing and sends nothing", async () => {
    client = null;
    const id = launch("Never mind");
    driveLaunch(id);
    await vi.waitFor(() => expect(homeLaunches.runtime(id)?.blocker?.code).toBe("engine_offline"));
    updateLaunch(id, (r) => ({ ...r, intent: "stop" }));
    haltLaunch(id);
    client = host.client();
    driveLaunch(id);
    await vi.waitFor(() => expect(homeLaunches.runtime(id)?.blocker?.code).toBe("stopped"));
    expect(host.puts).toBe(0);
    expect(host.prompts).toEqual([]);
  });

  it("leaves the idea in chat when the engine predates Home starts", async () => {
    client = stubClient({
      getHomeStartCapabilities: vi.fn(async () => { throw new LycaonApiError("missing", 404, "not_found"); }),
      reserveHomeStart: vi.fn(),
    }) as LycaonClient;
    const id = launch("Old engine");
    driveLaunch(id);
    await vi.waitFor(() => expect(homeLaunches.runtime(id)?.blocker?.code).toBe("host_outdated"));
    expect(vi.mocked(client.reserveHomeStart)).not.toHaveBeenCalled();
  });

  it("never replays into another host without an explicit send", async () => {
    const id = launch("Wrong engine");
    updateLaunch(id, (r) => ({ ...r, expectedHost: { storeId: "store-0", personId: "person-1" } }));
    driveLaunch(id);
    await vi.waitFor(() => expect(homeLaunches.runtime(id)?.blocker?.code).toBe("destination_changed"));
    expect(host.puts).toBe(0);
    updateLaunch(id, (r) => ({ ...r, destinationAccepted: true }));
    wakeLaunch(id);
    await vi.waitFor(() => expect(handoffs).toHaveLength(1));
  });

  it("holds the send for a missing file and sends after it is chosen again", async () => {
    const chip: LaunchChip = { kind: "bytes", itemId: crypto.randomUUID(), name: "notes.txt", mime: "text/plain", byteLength: 4, snapshot: "ready" };
    const id = launch("With notes", [chip]);
    driveLaunch(id);
    await vi.waitFor(() => expect(homeLaunches.runtime(id)?.blocker?.code).toBe("attachments"));
    expect(host.prompts).toEqual([]);
    expect(homeLaunches.record(id)?.chips[0]).toMatchObject({ snapshot: "missing" });

    const replacement = crypto.randomUUID();
    await stageLaunchBytes(replacement, new Blob(["text"], { type: "text/plain" }));
    updateLaunch(id, (r) => ({ ...r, chips: [{ ...chip, itemId: replacement, snapshot: "ready" }] }));
    wakeLaunch(id);
    await vi.waitFor(() => expect(handoffs).toHaveLength(1));
    expect(host.prompts[0]?.attachments).toHaveLength(1);
  });

  it("replaces an uncertain first prompt before sending the edit", async () => {
    const id = launch("First wording");
    host.reserved = true;
    host.phase = "session_bound";
    host.operation = id;
    host.revision = 2;
    updateLaunch(id, (r) => ({ ...r, sendAttempted: true, proposedText: "Second wording", hostState: host.view(id) }));
    driveLaunch(id);
    await vi.waitFor(() => expect(handoffs).toHaveLength(1));
    expect(host.prompts).toHaveLength(1);
    expect(host.prompts[0]?.text).toBe("Second wording");
    expect(host.prompts[0]?.operation_id).not.toBe(id);
  });

  it("turns an edit made after admission into the chat's next message", async () => {
    const id = launch("Original");
    host.reserved = true;
    host.phase = "session_bound";
    host.operation = id;
    host.admitted = true;
    host.revision = 3;
    updateLaunch(id, (r) => ({
      ...r, sendAttempted: true, proposedText: "Follow-up",
      hostState: { ...host.view(id), first_prompt: { operation_id: id, admitted: false }, revision: 2 },
    }));
    driveLaunch(id);
    await vi.waitFor(() => expect(handoffs).toEqual([{ launchId: id, sessionId: SESSION, followUp: "Follow-up" }]));
  });
});

describe("home launch persistence", () => {
  it("records the launch and consumes its composition in one write", async () => {
    const outbox = memoryHomeOutbox({
      format: 1, revision: 0, launches: [],
      composition: { id: "composition-1", revision: 4, text: "Saved", chips: [], updatedAt: 1 },
    });
    resetHomeLaunchesForTest(outbox);
    const committed = vi.fn();
    const id = submitHomeLaunch({ text: "Saved", mode: "agent", chips: [], compositionId: "composition-1", compositionRevision: 4 }, committed);
    await vi.waitFor(() => expect(committed).toHaveBeenCalledWith(id));
    await flushHomeLaunchWrites();
    expect(outbox.manifest().launches.map((r) => r.launchId)).toEqual([id]);
    expect(outbox.manifest().composition).toBeUndefined();
  });

  it("opens the other window's launch when it consumed the same revision first", async () => {
    const outbox = memoryHomeOutbox({
      format: 1, revision: 1,
      launches: [{
        launchId: "winner", localRevision: 1, createdAt: 1, compositionId: "c", compositionRevision: 2,
        text: "Same", mode: "agent", chips: [], intent: "send", promptOperationId: "winner",
      }],
    });
    resetHomeLaunchesForTest(outbox);
    const committed = vi.fn();
    const id = submitHomeLaunch({ text: "Same", mode: "agent", chips: [], compositionId: "c", compositionRevision: 2 }, committed);
    await vi.waitFor(() => expect(committed).toHaveBeenCalledWith("winner"));
    expect(homeLaunches.record(id)).toBeUndefined();
    expect(outbox.manifest().launches).toHaveLength(1);
  });

  it("proceeds from memory and reports when local storage fails", async () => {
    const outbox = memoryHomeOutbox();
    outbox.commit = async () => { throw new Error("disk full"); };
    resetHomeLaunchesForTest(outbox);
    const committed = vi.fn();
    const id = submitHomeLaunch({ text: "Unsaved", mode: "agent", chips: [], compositionId: "c", compositionRevision: 1 }, committed);
    await vi.waitFor(() => expect(committed).toHaveBeenCalledWith(id));
    expect(homeLaunches.runtime(id)).toMatchObject({ save: "failed", saveError: "disk full" });
    expect(homeLaunches.record(id)?.text).toBe("Unsaved");
  });

  it("never lets an older host view replace a newer one", () => {
    const id = launch("Ordered");
    applyHostState(id, { ...host.view(id), phase: "session_bound", revision: 5 });
    applyHostState(id, { ...host.view(id), phase: "reserved", revision: 3 });
    expect(homeLaunches.record(id)?.hostState).toMatchObject({ phase: "session_bound", revision: 5 });
  });
});
