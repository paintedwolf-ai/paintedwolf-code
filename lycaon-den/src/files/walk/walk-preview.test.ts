import { afterEach, expect, it, vi } from "vitest";
import { applySourceChangesEvent } from "../source/source-events.ts";
import { stubClient } from "../../test/client-fixture.ts";
import {
  cachedWalkPreview,
  refreshWalkPreviews,
  resetWalkPreviewsForTests,
  subscribeWalkPreview,
} from "./walk-preview.ts";

afterEach(() => { resetWalkPreviewsForTests(); vi.useRealTimers(); });

it("batches visible turns without fetching Walk history and scopes session refreshes", async () => {
  vi.useFakeTimers();
  const read = vi.fn(async (_project: string, _session: string, ids: string[]) => ids.map((id) => ({ message_id: id, turn: 2, steps: 2, items: 1 })));
  const history = vi.fn();
  const client = stubClient({ getProjectSourceWalkSummary: read, listProjectSourceWalk: history });
  const notify = vi.fn();
  const release = Array.from({ length: 205 }, (_, index) => subscribeWalkPreview(client, "p", "s", `message-${index}`, notify));
  await vi.advanceTimersByTimeAsync(450);
  expect(read.mock.calls.map((args) => args[2].length)).toEqual([100, 100, 5]);
  expect(history).not.toHaveBeenCalled();
  expect(notify).toHaveBeenLastCalledWith({ message_id: "message-204", turn: 2, steps: 2, items: 1 });
  refreshWalkPreviews("p", "other");
  await vi.advanceTimersByTimeAsync(450);
  expect(read).toHaveBeenCalledTimes(3);
  for (const stop of release) stop();
  refreshWalkPreviews("p", "s");
  await vi.advanceTimersByTimeAsync(450);
  expect(read).toHaveBeenCalledTimes(3);
});

it("answers a remounted card from the host's last answer until sources change", async () => {
  vi.useFakeTimers();
  const read = vi.fn(async (_project: string, _session: string, ids: string[]) =>
    ids.filter((id) => id !== "quiet").map((id) => ({ message_id: id, turn: 2, steps: 2, items: 1 })));
  const client = stubClient({ getProjectSourceWalkSummary: read });
  const stopTurn = subscribeWalkPreview(client, "p", "s", "turn", vi.fn());
  const stopQuiet = subscribeWalkPreview(client, "p", "s", "quiet", vi.fn());
  await vi.advanceTimersByTimeAsync(450);
  stopTurn();
  stopQuiet();

  expect(cachedWalkPreview("p", "s", "turn")).toEqual({ message_id: "turn", turn: 2, steps: 2, items: 1 });
  expect(cachedWalkPreview("p", "s", "quiet")).toBeNull();
  expect(cachedWalkPreview("p", "s", "unasked")).toBeUndefined();

  const remounted = vi.fn();
  const stop = subscribeWalkPreview(client, "p", "s", "turn", remounted);
  expect(remounted).toHaveBeenCalledWith({ message_id: "turn", turn: 2, steps: 2, items: 1 });
  await vi.advanceTimersByTimeAsync(450);
  expect(read).toHaveBeenCalledTimes(1);

  refreshWalkPreviews("p", "s");
  await vi.advanceTimersByTimeAsync(450);
  expect(read).toHaveBeenCalledTimes(2);
  stop();
});

it("refreshes a zero-step turn when a commit arrives without a file batch", async () => {
  vi.useFakeTimers();
  let steps = 0;
  const read = vi.fn(async () => [{message_id: "prompt", turn: 1, steps, items: 0}]);
  const notify = vi.fn();
  const stop = subscribeWalkPreview(stubClient({getProjectSourceWalkSummary: read}), "git-project", "session", "prompt", notify);
  await vi.advanceTimersByTimeAsync(450);
  expect(notify).toHaveBeenLastCalledWith(expect.objectContaining({steps: 0}));
  steps = 1;
  applySourceChangesEvent({project_id: "git-project", workspace_id: "workspace", workspace_kind: "project", resync: false, git_changed: true, changes: []});
  await vi.advanceTimersByTimeAsync(450);
  expect(notify).toHaveBeenLastCalledWith(expect.objectContaining({steps: 1, items: 0}));
  expect(read).toHaveBeenCalledTimes(2);
  stop();
});
