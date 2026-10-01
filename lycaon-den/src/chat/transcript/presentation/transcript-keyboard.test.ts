// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { createRoot, createSignal } from "solid-js";
import { createTranscriptKeyboard, lastTranscriptRow } from "./transcript-keyboard.ts";
import { invokeCommand, resetDispatcherForTests } from "../../../shortcuts/dispatcher.ts";

let dispose: (() => void) | undefined;
afterEach(() => { dispose?.(); resetDispatcherForTests(); vi.unstubAllGlobals(); document.body.replaceChildren(); });

it("finds the last message row by transcript index, not the first DOM match", () => {
  const root = document.createElement("div");
  root.innerHTML = `
    <div class="transcript-viewport-row" data-index="4" data-msg-id="e"></div>
    <div class="transcript-viewport-row" data-index="2" data-msg-id="c"></div>
    <div class="transcript-viewport-row" data-index="5" data-time-row="t"></div>
    <div class="transcript-viewport-row" data-index="3" data-msg-id="d"></div>`;
  expect(lastTranscriptRow(root)?.dataset.msgId).toBe("e");
  expect(lastTranscriptRow(document.createElement("div"))).toBeNull();
});

it("reveals logical messages and does not reclaim focus after the user leaves", async () => {
  const frames: FrameRequestCallback[] = [];
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => { frames.push(callback); return frames.length; });
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
  const viewport = document.createElement("div"); viewport.tabIndex = -1;
  const input = document.createElement("input");
  document.body.append(viewport, input);
  const reveal = vi.fn();
  createRoot(stop => {
    dispose = stop;
    createTranscriptKeyboard({ active: () => true, rows: () => [{ key: "a", kind: "user" }, { key: "time", kind: "time" }, { key: "b", kind: "assistant" }],
      root: () => viewport, viewport: () => viewport, reveal, loadEarlier: async () => {}, loadLater: async () => {}, reportError: error => { throw error; } });
  });
  viewport.focus();
  invokeCommand("chat.nextMessage");
  expect(reveal).toHaveBeenLastCalledWith(0);
  const row = document.createElement("div"); row.tabIndex = -1; row.className = "transcript-viewport-row"; row.dataset.msgId = "a";
  viewport.append(row);
  frames.shift()!(0);
  expect(document.activeElement).toBe(row);
  invokeCommand("chat.nextMessage");
  expect(reveal).toHaveBeenLastCalledWith(2);
  input.focus();
  const second = row.cloneNode() as HTMLElement; second.dataset.msgId = "b"; viewport.append(second);
  frames.shift()!(0);
  expect(document.activeElement).toBe(input);
});

it("loads older messages before choosing the previous row", async () => {
  const reveal = vi.fn();
  vi.stubGlobal("requestAnimationFrame", vi.fn(() => 1));
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
  const viewport = document.createElement("div"); viewport.tabIndex = -1; document.body.append(viewport);
  const row = document.createElement("div"); row.tabIndex = -1; row.dataset.msgId = "new"; viewport.append(row);
  const [rows, setRows] = createSignal([{ key: "new", kind: "user" }]);
  const load = vi.fn(async () => { setRows([{ key: "old", kind: "assistant" }, ...rows()]); });
  createRoot(stop => {
    dispose = stop;
    createTranscriptKeyboard({ active: () => true, rows, root: () => viewport, viewport: () => viewport, reveal,
      loadEarlier: load, loadLater: async () => {}, reportError: error => { throw error; } });
  });
  row.focus();
  invokeCommand("chat.previousMessage");
  await Promise.resolve();
  expect(load).toHaveBeenCalledWith(false);
  expect(reveal).toHaveBeenCalledWith(0);
});

it("loads the history leading to the live tail before choosing the next row", async () => {
  const reveal = vi.fn();
  vi.stubGlobal("requestAnimationFrame", vi.fn(() => 1));
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
  const viewport = document.createElement("div"); viewport.tabIndex = -1; document.body.append(viewport);
  const row = document.createElement("div"); row.tabIndex = -1; row.dataset.msgId = "old"; viewport.append(row);
  const [rows, setRows] = createSignal([{ key: "old", kind: "user" }]);
  const later = vi.fn(async () => { setRows([...rows(), { key: "newer", kind: "assistant" }]); });
  createRoot(stop => {
    dispose = stop;
    createTranscriptKeyboard({ active: () => true, rows, root: () => viewport, viewport: () => viewport, reveal,
      loadEarlier: async () => {}, loadLater: later, reportError: error => { throw error; } });
  });
  row.focus();
  invokeCommand("chat.nextMessage");
  await Promise.resolve();
  expect(later).toHaveBeenCalledWith(false);
  expect(reveal).toHaveBeenCalledWith(1);
});
