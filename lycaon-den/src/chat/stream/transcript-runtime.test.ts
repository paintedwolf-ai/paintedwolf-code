// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { watchTranscriptRuntime } from "./transcript-runtime.ts";

it("retains scroll subscriptions across session snapshots and incidental binding reads", () => {
  const [session, setSession] = createSignal({ id: "chat", title: "First" });
  const [panel, setPanel] = createSignal(0);
  const stream = document.createElement("div");
  const stop = vi.fn();
  const bind = vi.fn(() => { panel(); return stop; });
  const dispose = createRoot(dispose => {
    watchTranscriptRuntime({ active: () => true, hydrating: () => false,
      sessionId: () => "chat", currentSessionId: () => session().id, stream: () => stream, bind });
    return dispose;
  });
  try {
    expect(bind).toHaveBeenCalledTimes(1);
    for (let i = 0; i < 100; i++) { setSession({ id: "chat", title: String(i) }); setPanel(i); }
    expect(bind).toHaveBeenCalledTimes(1);
    expect(stop).not.toHaveBeenCalled();
  } finally { dispose(); }
  expect(stop).toHaveBeenCalledTimes(1);
});

describe("transcript runtime lifecycle", () => {
  it("releases and rebinds on hydration, visibility, session and viewport changes", () => {
    const [active, setActive] = createSignal(true);
    const [hydrating, setHydrating] = createSignal(false);
    const [sessionId, setSessionId] = createSignal("first");
    const [currentSessionId, setCurrentSessionId] = createSignal("first");
    const [stream, setStream] = createSignal<HTMLElement | undefined>(document.createElement("div"));
    const stop = vi.fn(), bind = vi.fn(() => stop);
    const dispose = createRoot(dispose => {
      watchTranscriptRuntime({ active, hydrating, sessionId, currentSessionId, stream, bind });
      return dispose;
    });
    try {
      expect(bind).toHaveBeenCalledTimes(1);
      setHydrating(true); expect(stop).toHaveBeenCalledTimes(1);
      setHydrating(false); expect(bind).toHaveBeenCalledTimes(2);
      setActive(false); expect(stop).toHaveBeenCalledTimes(2);
      setActive(true); expect(bind).toHaveBeenCalledTimes(3);
      setSessionId("second"); expect(stop).toHaveBeenCalledTimes(3);
      setCurrentSessionId("second"); expect(bind).toHaveBeenCalledTimes(4);
      setStream(undefined); expect(stop).toHaveBeenCalledTimes(4);
      setStream(document.createElement("div")); expect(bind).toHaveBeenCalledTimes(5);
    } finally { dispose(); }
    expect(stop).toHaveBeenCalledTimes(5);
  });
});
