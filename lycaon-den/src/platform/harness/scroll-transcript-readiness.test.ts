// @vitest-environment jsdom
import { runInNewContext } from "node:vm";
import { afterEach, expect, it, vi } from "vitest";
import pageSource from "../../../src-tauri/crates/webkit-harness/src/scroll_thread_page.js?raw";

function fixture(admission: unknown = { ok: true, sessionId: "current", lastMessageId: "tail" }) {
  vi.useFakeTimers();
  const harness = {
    goto: vi.fn(async () => admission),
    state: () => ({ sessionId: "current" }),
    transcript: () => ({ text: "fixture transcript" }),
  };
  const page = {} as { __scrollThread: { admitTranscript(options: object): Promise<void> } };
  runInNewContext(pageSource, {
    window: page, document, CSS: { escape: (value: string) => value }, __harness: harness, setTimeout,
  });
  return page.__scrollThread;
}

afterEach(() => {
  document.body.replaceChildren();
  vi.useRealTimers();
});

it("waits for the exact admitted tail in its session rather than unrelated mounted rows", async () => {
  document.body.innerHTML = `
    <div data-testid="chat-stream" data-session-id="other"><div data-msg-id="tail"></div></div>
    <div data-testid="chat-stream" data-session-id="current"><div data-msg-id="earlier"></div></div>`;
  const driver = fixture();
  let delivered = false;
  const pending = driver.admitTranscript({ turns: 5 }).then(() => { delivered = true; });
  await vi.advanceTimersByTimeAsync(1000);
  expect(delivered).toBe(false);
  document.querySelector('[data-session-id="current"]')!.innerHTML += '<div data-msg-id="tail"></div>';
  await vi.advanceTimersByTimeAsync(250);
  await pending;
  expect(delivered).toBe(true);
});

it("fails with delivery evidence when acknowledged rows never mount", async () => {
  document.body.innerHTML = '<div data-testid="chat-stream" data-session-id="current"></div>';
  const driver = fixture();
  const result = expect(driver.admitTranscript({ turns: 5 })).rejects.toThrow(
    /admitted transcript tail did not mount:.*"lastMessageId":"tail".*fixture transcript/,
  );
  await vi.advanceTimersByTimeAsync(15_000);
  await result;
});

it.each([null, { ok: false }, { ok: true }, { ok: true, sessionId: "current" }])(
  "rejects an incomplete admission receipt (%j)", async (admission) => {
    await expect(fixture(admission).admitTranscript({ turns: 5 })).rejects.toThrow("scroll transcript admission:");
  },
);
