import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createSeenMarker } from "./seen-marker.ts";

type Harness = {
  setReadable: (id: string | null) => void;
  setRevision: (rev: string) => void;
  mark: ReturnType<typeof vi.fn>;
  dispose: () => void;
};

function harness(
  readableAt: string | null = "s1",
  revisionAt = "idle:1",
): Harness {
  const [readable, setReadable] = createSignal<string | null>(readableAt);
  const [revision, setRevision] = createSignal(revisionAt);
  const mark = vi.fn(() => Promise.resolve());
  const dispose = createSeenMarker({
    readable,
    revision,
    mark,
  });
  return { setReadable, setRevision, mark, dispose };
}

describe("createSeenMarker", () => {
  it("marks the readable session once", () => {
    const h = harness();
    expect(h.mark).toHaveBeenCalledExactlyOnceWith("s1");
    h.dispose();
  });

  it("does not re-mark while nothing has changed", () => {
    const h = harness();
    h.setRevision("idle:1");
    expect(h.mark).toHaveBeenCalledOnce();
    h.dispose();
  });

  it("re-marks when a turn lands while the person is watching", () => {
    const h = harness();
    // A finish observed on screen is marked read immediately.
    h.setRevision("idle:2");
    expect(h.mark).toHaveBeenCalledTimes(2);
    h.dispose();
  });

  it("marks nothing while no conversation is readable", () => {
    const h = harness(null);
    expect(h.mark).not.toHaveBeenCalled();
    h.setRevision("idle:2");
    expect(h.mark).not.toHaveBeenCalled();
    h.dispose();
  });

  it("stops marking when the conversation stops being readable", () => {
    const h = harness();
    h.setReadable(null);
    h.setRevision("idle:2");
    expect(h.mark).toHaveBeenCalledOnce();
    h.dispose();
  });

  it("marks the new session after a switch", () => {
    const h = harness();
    h.setReadable("s2");
    expect(h.mark).toHaveBeenLastCalledWith("s2");
    h.dispose();
  });

  it("survives a failed post and re-marks on the next change", async () => {
    const [readable] = createSignal<string | null>("s1");
    const [revision, setRevision] = createSignal("idle:1");
    const mark = vi.fn(() => Promise.reject(new Error("offline")));
    const dispose = createSeenMarker({ readable, revision, mark });
    await Promise.resolve();
    setRevision("idle:2");
    expect(mark).toHaveBeenCalledTimes(2);
    dispose();
  });

  it("reports each time a session becomes readable, before its stamp posts", () => {
    const [readable, setReadable] = createSignal<string | null>("s1");
    const [revision] = createSignal("idle:1");
    const calls: string[] = [];
    const dispose = createSeenMarker({
      readable,
      revision,
      mark: (id) => {
        calls.push(`mark:${id}`);
        return Promise.resolve();
      },
      onReadable: (id) => calls.push(`readable:${id}`),
    });
    setReadable(null);
    // Back to the same chat without new rows: no fresh stamp, but a fresh look.
    setReadable("s1");
    setReadable("s2");
    expect(calls).toEqual(["readable:s1", "mark:s1", "readable:s1", "readable:s2", "mark:s2"]);
    dispose();
  });

  it("stops marking after dispose", () => {
    const h = harness();
    h.dispose();
    h.setRevision("idle:2");
    expect(h.mark).toHaveBeenCalledOnce();
  });
});
