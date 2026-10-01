import { afterEach, expect, it, vi } from "vitest";
import { FrameMeasurements } from "./frame-measurements.ts";

afterEach(() => vi.unstubAllGlobals());

function frames() {
  const pending = new Map<number, FrameRequestCallback>();
  let serial = 0;
  vi.stubGlobal("requestAnimationFrame", (run: FrameRequestCallback) => {
    pending.set(++serial, run);
    return serial;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => pending.delete(id));
  return () => {
    const batch = [...pending.values()];
    pending.clear();
    for (const run of batch) run(0);
  };
}

it("coalesces requests and reads every host before writing any of them", () => {
  const flush = frames();
  const log: string[] = [];
  let size = 10;
  const updates = new FrameMeasurements(
    (host: string) => { log.push(`read ${host}`); return size; },
    (host, measured) => { log.push(`write ${host} ${measured}`); size++; },
  );
  updates.request("chat");
  updates.request("files");
  updates.request("chat");
  size = 20;
  flush();
  expect(log).toEqual(["read chat", "read files", "write chat 20", "write files 20"]);
  flush();
  expect(log).toHaveLength(4);
});

it("discards disposed hosts and defers requests made during publication", () => {
  const flush = frames();
  const written: string[] = [];
  const updates = new FrameMeasurements(
    (host: string) => host,
    (host) => {
      written.push(host);
      if (host === "chat") {
        updates.cancel("removed");
        updates.request("next");
      }
    },
  );
  updates.request("canceled");
  updates.cancel("canceled");
  updates.request("chat");
  updates.request("removed");
  flush();
  expect(written).toEqual(["chat"]);
  flush();
  expect(written).toEqual(["chat", "next"]);
  updates.request("disposed");
  updates.clear();
  flush();
  expect(written).toEqual(["chat", "next"]);
});
