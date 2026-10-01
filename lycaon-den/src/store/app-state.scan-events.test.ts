import { createComputed, createRoot } from "solid-js";
import { describe, expect, it } from "vitest";
import type { CodeScanEvent } from "../api/types.ts";
import { createAppStore } from "./app-state.ts";

describe("scan event occurrences", () => {
  it.each([
    ["pending", "running", "complete"],
    ["running", "failed", "pending", "running", "complete"],
    ["complete", "complete", "complete"],
  ] as const)("publishes each occurrence without mutating earlier summaries: %j", (...statuses) => {
    createRoot((dispose) => {
      try {
        const store = createAppStore();
        const seen: CodeScanEvent[] = [];
        createComputed(() => {
          const event = store.state.latestCodeScan;
          if (event) seen.push(event);
        });
        for (const status of statuses) {
          store.actions.addCodeScan({ scan_id: "same-scan", status, categories: ["sast"], findings_count: 1, long_running: false });
        }
        expect(seen.map((event) => event.status)).toEqual(statuses);
        expect(new Set(seen).size).toBe(statuses.length);
      } finally {
        dispose();
      }
    });
  });

  it("drops facts omitted by the next scan instead of carrying them forward", () => {
    const store = createAppStore();
    store.actions.addCodeScan({ scan_id: "failed-scan", status: "failed", categories: ["sast"], findings_count: 0, long_running: true, error: "scanner unavailable" });
    store.actions.addCodeScan({ scan_id: "next-scan", status: "complete", categories: ["secret"], findings_count: 0, long_running: false });
    expect(store.state.latestCodeScan?.error).toBeUndefined();
    expect(store.state.latestCodeScan?.categories).toEqual(["secret"]);
    expect(store.state.latestCodeScan?.long_running).toBe(false);
  });
});
