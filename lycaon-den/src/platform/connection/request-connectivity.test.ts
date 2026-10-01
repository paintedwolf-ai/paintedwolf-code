import { afterEach, expect, it, vi } from "vitest";
import {
  confirmBackendReachability,
  noteBackendReachable,
  setBackendReachabilityObserver,
} from "./request-connectivity.ts";

function deferred() {
  let resolve!: (value: boolean) => void;
  const promise = new Promise<boolean>((done) => { resolve = done; });
  return { promise, resolve };
}

afterEach(() => setBackendReachabilityObserver(null));

it("does not let an old failed probe overwrite a successful request", async () => {
  const reachable = vi.fn();
  const unreachable = vi.fn();
  setBackendReachabilityObserver({ reachable, unreachable });
  const probe = deferred();
  const result = confirmBackendReachability(() => probe.promise);
  noteBackendReachable();
  probe.resolve(false);

  expect(await result).toBe("unreachable");
  expect(reachable).toHaveBeenCalledOnce();
  expect(unreachable).not.toHaveBeenCalled();
});

it("retains the newer probe's outcome when probes finish out of order", async () => {
  const reachable = vi.fn();
  const unreachable = vi.fn();
  setBackendReachabilityObserver({ reachable, unreachable });
  const oldProbe = deferred();
  const oldResult = confirmBackendReachability(() => oldProbe.promise);

  expect(await confirmBackendReachability(async () => false)).toBe("unreachable");
  oldProbe.resolve(true);
  expect(await oldResult).toBe("reachable");
  expect(unreachable).toHaveBeenCalledOnce();
  expect(reachable).not.toHaveBeenCalled();
});

it("does not deliver an old connection's probe to a replacement observer", async () => {
  setBackendReachabilityObserver({ reachable: vi.fn(), unreachable: vi.fn() });
  const probe = deferred();
  const result = confirmBackendReachability(() => probe.promise);
  const replacement = { reachable: vi.fn(), unreachable: vi.fn() };
  setBackendReachabilityObserver(replacement);
  probe.resolve(false);

  await result;
  expect(replacement.reachable).not.toHaveBeenCalled();
  expect(replacement.unreachable).not.toHaveBeenCalled();
});

it("reports a thrown probe as unreachable", async () => {
  const unreachable = vi.fn();
  setBackendReachabilityObserver({ reachable: vi.fn(), unreachable });

  const probe = vi.fn<() => Promise<boolean>>().mockRejectedValue(new Error("Unavailable"));
  expect(await confirmBackendReachability(probe))
    .toBe("unreachable");
  expect(unreachable).toHaveBeenCalledOnce();
});
