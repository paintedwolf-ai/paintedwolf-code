import { describe, expect, it, vi } from "vitest";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import { noticeScopeKey } from "../../notices/notice-scope.ts";
import type { NativeUpdateState, UpdateService } from "./update-service.ts";
import { mountUpdateNotice } from "./mount-update-notice.ts";

const state = (
  over: Partial<NativeUpdateState> = {},
): NativeUpdateState => ({
  phase: "idle",
  revision: 0,
  current_version: "1.0.0",
  channel: "stable",
  install_source: "direct_download",
  checks_enabled: true,
  downloaded_bytes: 0,
  total_bytes: null,
  rollout_eligibility: "not_applicable",
  ...over,
});

describe("mountUpdateNotice", () => {
  it("publishes each discovered version once at app scope", async () => {
    let deliver: ((next: NativeUpdateState) => void) | undefined;
    const service: UpdateService = {
      getState: vi.fn(async () => state()),
      setChecksEnabled: vi.fn(async () => state()),
      setChannel: vi.fn(async () => state()),
      check: vi.fn(async () => state()),
      install: vi.fn(async () => state()),
      subscribe: vi.fn(async (handler) => {
        deliver = handler;
        return () => {};
      }),
    };
    const notices = createNoticeStore();
    const stop = mountUpdateNotice({ notices, service });
    await vi.waitFor(() => expect(deliver).toBeTypeOf("function"));

    const available = state({ phase: "available", available_version: "1.1.0" });
    deliver?.(available);
    deliver?.(available);

    const rows = notices.index().get(noticeScopeKey(APP_SCOPE)) ?? [];
    expect(rows).toHaveLength(1);
    expect(rows[0]?.code).toBe("update_available");
    stop();
  });

  it("continues when the event stream is unavailable", async () => {
    const service: UpdateService = {
      getState: vi.fn(async () => state()),
      setChecksEnabled: vi.fn(async () => state()),
      setChannel: vi.fn(async () => state()),
      check: vi.fn(async () => state()),
      install: vi.fn(async () => state()),
      subscribe: vi.fn(async () => {
        throw new Error("update_event_stream_unavailable");
      }),
    };
    const stop = mountUpdateNotice({ notices: createNoticeStore(), service });

    await vi.waitFor(() => expect(service.getState).toHaveBeenCalledOnce());
    stop();
  });
  it("ignores delayed offers after newer state or disposal", async () => {
    let deliver: ((next: NativeUpdateState) => void) | undefined;
    const service: UpdateService = {
      getState: async () => state(),
      setChecksEnabled: async () => state(),
      setChannel: async () => state(),
      check: async () => state(),
      install: async () => state(),
      subscribe: async (handler) => { deliver = handler; return () => {}; },
    };
    const notices = createNoticeStore();
    const stop = mountUpdateNotice({ notices, service });
    await vi.waitFor(() => expect(deliver).toBeTypeOf("function"));
    deliver?.(state({ revision: 3, phase: "restart_required" }));
    deliver?.(state({ revision: 2, phase: "available", available_version: "1.1.0" }));
    expect(notices.index().get(noticeScopeKey(APP_SCOPE)) ?? []).toHaveLength(0);
    stop();
    deliver?.(state({ revision: 4, phase: "available", available_version: "1.2.0" }));
    expect(notices.index().get(noticeScopeKey(APP_SCOPE)) ?? []).toHaveLength(0);
  });

});
