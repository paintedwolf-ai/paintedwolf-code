import { stubClient } from "../../test/client-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  fileSummariesEnabled,
  fileSummariesSettingKnown,
  refreshFileSummariesSetting,
  resetFileSummariesSetting,
  saveFileSummariesEnabled,
} from "./file-summary-settings.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

describe("File summaries settings", () => {
  beforeEach(resetFileSummariesSetting);

  it("rejects a read that completes after backend reset", async () => {
    const old = deferred<{ enabled: boolean }>();
    const reading = refreshFileSummariesSetting(stubClient({ getFileSummariesSettings: () => old.promise }));
    resetFileSummariesSetting();
    await refreshFileSummariesSetting(stubClient({ getFileSummariesSettings: async () => ({ enabled: false }) }));
    old.resolve({ enabled: true });
    await reading;
    expect(fileSummariesEnabled()).toBe(false);
  });

  it("keeps the new backend's save when an old save finishes later", async () => {
    const old = deferred<{ enabled: boolean }>();
    const saving = saveFileSummariesEnabled(stubClient({ updateFileSummariesSettings: () => old.promise }), true);
    resetFileSummariesSetting();
    const updateFileSummariesSettings = vi.fn().mockResolvedValue({ enabled: false });
    await saveFileSummariesEnabled(stubClient({ updateFileSummariesSettings }), false);
    old.resolve({ enabled: true });
    await saving;
    expect(updateFileSummariesSettings).toHaveBeenCalledWith({ enabled: false });
    expect(fileSummariesEnabled()).toBe(false);
  });

  it("ignores a stale GET that finishes after a successful save", async () => {
    const old = deferred<{ enabled: boolean }>();
    const client = stubClient({ getFileSummariesSettings: () => old.promise, updateFileSummariesSettings: async () => ({ enabled: false }) });
    const reading = refreshFileSummariesSetting(client);
    await saveFileSummariesEnabled(client, false);
    old.resolve({ enabled: true });
    await reading;
    expect(fileSummariesEnabled()).toBe(false);
  });

  it("hydrates the backend source of truth", async () => {
    const client = stubClient({
      getFileSummariesSettings: vi.fn().mockResolvedValue({ enabled: false }),
    });
    await refreshFileSummariesSetting(client);
    expect(fileSummariesSettingKnown()).toBe(true);
    expect(fileSummariesEnabled()).toBe(false);
  });

  it("uses the same PUT path for Settings and the View menu", async () => {
    const updateFileSummariesSettings = vi.fn().mockResolvedValue({ enabled: false });
    const client = stubClient({ updateFileSummariesSettings });
    await saveFileSummariesEnabled(client, false);
    expect(updateFileSummariesSettings).toHaveBeenCalledWith({ enabled: false });
    expect(fileSummariesEnabled()).toBe(false);
  });

  it("shows an enable at once, before the host answers", async () => {
    const put = deferred<{ enabled: boolean }>();
    const client = stubClient({
      updateFileSummariesSettings: vi
        .fn()
        .mockResolvedValueOnce({ enabled: false })
        .mockImplementationOnce(() => put.promise),
    });
    await saveFileSummariesEnabled(client, false);
    const saving = saveFileSummariesEnabled(client, true);
    expect(fileSummariesEnabled()).toBe(true);
    put.resolve({ enabled: true });
    await saving;
    expect(fileSummariesEnabled()).toBe(true);
  });

  it("coalesces repeat toggles to the latest request", async () => {
    const first = deferred<{ enabled: boolean }>();
    const updateFileSummariesSettings = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementation(async (req: { enabled: boolean }) => req);
    const client = stubClient({ updateFileSummariesSettings });

    const off = saveFileSummariesEnabled(client, false);
    const on = saveFileSummariesEnabled(client, true);
    const offAgain = saveFileSummariesEnabled(client, false);
    expect(fileSummariesEnabled()).toBe(false);

    first.resolve({ enabled: false });
    await Promise.all([off, on, offAgain]);
    expect(updateFileSummariesSettings.mock.calls.map(([req]) => req)).toEqual([
      { enabled: false },
      { enabled: false },
    ]);
    expect(fileSummariesEnabled()).toBe(false);
  });

  it("restores the host's value when the latest update fails", async () => {
    const client = stubClient({
      updateFileSummariesSettings: vi.fn().mockRejectedValue(new Error("offline")),
    });
    await expect(saveFileSummariesEnabled(client, false)).rejects.toThrow("offline");
    expect(fileSummariesEnabled()).toBe(true);
  });

  it("keeps a newer request when an earlier one fails", async () => {
    const first = deferred<{ enabled: boolean }>();
    const updateFileSummariesSettings = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementation(async (req: { enabled: boolean }) => req);
    const client = stubClient({ updateFileSummariesSettings });

    const off = saveFileSummariesEnabled(client, false);
    const on = saveFileSummariesEnabled(client, true);
    first.reject(new Error("offline"));
    await Promise.all([off, on]);
    expect(fileSummariesEnabled()).toBe(true);
    expect(updateFileSummariesSettings).toHaveBeenLastCalledWith({ enabled: true });
  });

  it("a refresh during a save keeps showing the request", async () => {
    const put = deferred<{ enabled: boolean }>();
    const client = stubClient({
      updateFileSummariesSettings: vi.fn(() => put.promise),
      getFileSummariesSettings: vi.fn().mockResolvedValue({ enabled: true }),
    });
    const saving = saveFileSummariesEnabled(client, false);
    await refreshFileSummariesSetting(client);
    expect(fileSummariesEnabled()).toBe(false);
    put.resolve({ enabled: false });
    await saving;
    expect(fileSummariesEnabled()).toBe(false);
  });
});
