import { beforeEach, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { SourceWalkResponse } from "../../api/types.ts";
import { loadWalk, resetWalkLoadsForTests } from "./walk-loader.ts";
import { prepareWalk, resetWalkPrefetchForTests } from "./walk-prefetch.ts";

beforeEach(() => { resetWalkPrefetchForTests(); resetWalkLoadsForTests(); });
const empty: SourceWalkResponse = { baseline: "session:s1", files: [], git_changes: [], commands: [], turns: [], commit_available: false, next_cursor: undefined };

it("bounds speculative entry acquisition while a selected walk can join the same request", async () => {
  let release!: (value: SourceWalkResponse) => void;
  const gate = new Promise<SourceWalkResponse>(resolve => { release = resolve; });
  const listProjectSourceWalk = vi.fn().mockReturnValue(gate);
  const client = stubClient({ listProjectSourceWalk });
  for (let index = 0; index < 10; index++) prepareWalk(client, `p${index}`, "s");
  await vi.waitFor(() => expect(listProjectSourceWalk).toHaveBeenCalledTimes(2));
  const selected = loadWalk(client, "p0", "s");
  expect(listProjectSourceWalk).toHaveBeenCalledTimes(2);
  release(empty);
  await selected;
  await vi.waitFor(() => expect(listProjectSourceWalk).toHaveBeenCalledTimes(10));
});

it("replaces obsolete queued entry targets for the same project", async () => {
  let release!: (value: SourceWalkResponse) => void;
  const gate = new Promise<SourceWalkResponse>(resolve => { release = resolve; });
  const listProjectSourceWalk = vi.fn().mockReturnValue(gate);
  const client = stubClient({ listProjectSourceWalk });
  prepareWalk(client, "busy1", "s");
  prepareWalk(client, "busy2", "s");
  prepareWalk(client, "p", "old");
  prepareWalk(client, "p", "latest");
  release(empty);
  await vi.waitFor(() => expect(listProjectSourceWalk).toHaveBeenCalledTimes(3));
  expect(listProjectSourceWalk.mock.calls[2]?.[1].sessionId).toBe("latest");
});
