import { afterEach, describe, expect, it, vi } from "vitest";
import { postDenPerfEvents } from "./den-perf-capture.ts";

vi.mock("../../settings/system/debug-prefs.ts", () => ({
  fullDebugLoggingPref: vi.fn(() => false),
}));

vi.mock("../../platform/connection/backend.ts", () => ({
  getBackendConnection: vi.fn(() => null),
}));

vi.mock("../../api/http.ts", () => ({
  lycaonFetch: vi.fn(() => Promise.resolve(new Response(null, { status: 204 }))),
}));

describe("den-perf-capture", () => {
  afterEach(() => {
    vi.resetAllMocks();
    vi.useRealTimers();
  });

  it("no-ops when full debug logging is off", async () => {
    const { fullDebugLoggingPref } = await import("../../settings/system/debug-prefs.ts");
    const { lycaonFetch } = await import("../../api/http.ts");
    vi.mocked(fullDebugLoggingPref).mockReturnValue(false);

    postDenPerfEvents([{ event: "loop-stall", detail: { lag_ms: 200 } }]);

    expect(lycaonFetch).not.toHaveBeenCalled();
  });

  it("posts stall events when full debug logging is on and connected", async () => {
    const { fullDebugLoggingPref } = await import("../../settings/system/debug-prefs.ts");
    const { getBackendConnection } = await import("../../platform/connection/backend.ts");
    const { lycaonFetch } = await import("../../api/http.ts");
    vi.mocked(fullDebugLoggingPref).mockReturnValue(true);
    vi.mocked(getBackendConnection).mockReturnValue({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "tok",
    });

    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-30T12:00:00Z"));
    postDenPerfEvents([
      {
        event: "loop-stall",
        channel: "perf",
        detail: { lag_ms: 340, recent: "thumbnail.capture:start@-40" },
      },
    ]);

    expect(lycaonFetch).toHaveBeenCalledWith(
      expect.objectContaining({ apiToken: "tok" }),
      "/v1/debug/den-perf",
      expect.objectContaining({ method: "POST" }),
    );
    const init = vi.mocked(lycaonFetch).mock.calls[0]?.[2];
    expect(JSON.parse(String(init?.body))).toEqual({
      events: [{
        observed_at: "2026-09-30T12:00:00.000Z",
        channel: "perf",
        event: "loop-stall",
        detail: { lag_ms: 340, recent: "thumbnail.capture:start@-40" },
      }],
    });
  });
  it("preserves observation time and splits batches at the host limit", async () => {
    const { fullDebugLoggingPref } = await import("../../settings/system/debug-prefs.ts");
    const { getBackendConnection } = await import("../../platform/connection/backend.ts");
    const { lycaonFetch } = await import("../../api/http.ts");
    vi.mocked(fullDebugLoggingPref).mockReturnValue(true);
    vi.mocked(getBackendConnection).mockReturnValue({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok" });
    vi.mocked(lycaonFetch).mockResolvedValue(new Response(null, { status: 204 }));
    const events = Array.from({ length: 65 }, (_, index) => ({
      observed_at: "2026-09-30T11:59:59Z", event: "sync-summary",
      detail: { index, omitted: undefined },
    }));
    postDenPerfEvents(events);
    const batches = vi.mocked(lycaonFetch).mock.calls.map(([, , init]) =>
      JSON.parse(String(init?.body)).events,
    );
    expect(batches.map((batch) => batch.length)).toEqual([32, 32, 1]);
    expect(batches.flat()).toEqual(events.map(({ detail, ...event }) => ({
      ...event, channel: "perf", detail: { index: detail.index },
    })));
  });
});
