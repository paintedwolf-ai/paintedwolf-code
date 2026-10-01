import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { CacheSettingsPanel } from "./CacheSettingsPanel.tsx";
import { CACHE_SETTINGS_COPY } from "../../../settings/storage/cache-settings-copy.ts";
import type { LocalDataStatus } from "../../../api/types.ts";

const sampleStatus = (): LocalDataStatus => ({
  buckets: [
    { id: "web_index", present: true, bytes: 2048 },
    { id: "fetch_cache", present: false },
    { id: "osv_cache", present: false },
    { id: "modelfeed", present: false },
    { id: "browser_cache", present: false },
    { id: "debug_logs", present: false },
  ],
  workspace_caches: [],
});

describe("CacheSettingsPanel", () => {
  it("renders host buckets with mapped labels", async () => {
    const client = {
      getLocalData: vi.fn().mockResolvedValue(sampleStatus()),
      clearLocalData: vi.fn(),
    };

    const { getByTestId } = render(() => (
      <CacheSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("cache-bucket-web_index")).toBeTruthy();
    });
    expect(getByTestId("cache-bucket-web_index").textContent).toContain(
      "Web research index",
    );
    expect(getByTestId("cache-presence-web_index").textContent).toContain(
      "2.0 KB",
    );
    expect(getByTestId("cache-presence-fetch_cache").textContent).toContain(
      CACHE_SETTINGS_COPY.empty,
    );
    expect(client.getLocalData).toHaveBeenCalled();
  });

  it("requires confirm before clear and posts host ids from GET", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const clearLocalData = vi.fn().mockResolvedValue({
      results: [{ id: "web_index", ok: true }],
      workspace_cache_results: [],
    });
    const client = {
      getLocalData: vi
        .fn()
        .mockResolvedValueOnce(sampleStatus())
        .mockResolvedValueOnce({
          buckets: sampleStatus().buckets.map((b) =>
            b.id === "web_index" ? { ...b, present: false, bytes: 0 } : b,
          ),
          workspace_caches: [],
        }),
      clearLocalData,
    };

    const { getByTestId } = render(() => (
      <CacheSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("cache-clear-web_index")).toBeTruthy();
    });
    fireEvent.click(getByTestId("cache-clear-web_index"));

    await waitFor(() => {
      expect(confirmSpy).toHaveBeenCalled();
      expect(clearLocalData).toHaveBeenCalledWith({ buckets: ["web_index"] });
    });

    confirmSpy.mockRestore();
  });

  it("does not clear when confirm is cancelled", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    const clearLocalData = vi.fn();
    const client = {
      getLocalData: vi.fn().mockResolvedValue(sampleStatus()),
      clearLocalData,
    };

    const { getByTestId } = render(() => (
      <CacheSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("cache-clear-all")).toBeTruthy();
    });
    fireEvent.click(getByTestId("cache-clear-all"));

    await waitFor(() => {
      expect(confirmSpy).toHaveBeenCalled();
    });
    expect(clearLocalData).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });

  it("disables clear on empty buckets", async () => {
    const client = {
      getLocalData: vi.fn().mockResolvedValue(sampleStatus()),
      clearLocalData: vi.fn(),
    };

    const { getByTestId } = render(() => (
      <CacheSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("cache-clear-fetch_cache")).toBeTruthy();
    });
    expect(
      (getByTestId("cache-clear-fetch_cache") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (getByTestId("cache-clear-web_index") as HTMLButtonElement).disabled,
    ).toBe(false);
  });

  it("shows connect hint without client", () => {
    const { getByTestId, queryByTestId } = render(() => (
      <CacheSettingsPanel client={null} />
    ));
    expect(getByTestId("cache-connect-hint").textContent).toContain(
      CACHE_SETTINGS_COPY.connectHint,
    );
    expect(queryByTestId("cache-bucket-list")).toBeNull();
  });

  it("shows and independently clears retained workspace caches", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const clearLocalData = vi.fn().mockResolvedValue({
      results: [],
      workspace_cache_results: [{ id: "0123456789abcdef", ok: true }],
    });
    const client = {
      getLocalData: vi.fn().mockResolvedValue({
        ...sampleStatus(),
        workspace_caches: [
          {
            id: "0123456789abcdef",
            source_root: "/work/firefox",
            logical_bytes: 30 * 1024 ** 3,
            allocated_bytes: 12 * 1024 ** 3,
            last_used: "2026-08-01T00:00:00Z",
          },
        ],
      }),
      clearLocalData,
    };
    const { getByTestId } = render(() => (
      <CacheSettingsPanel client={client as never} />
    ));

    await waitFor(() =>
      expect(getByTestId("workspace-cache-0123456789abcdef")).toBeTruthy(),
    );
    expect(
      getByTestId("workspace-cache-0123456789abcdef").textContent,
    ).toContain("30.0 GB");
    fireEvent.click(getByTestId("workspace-cache-clear-0123456789abcdef"));
    await waitFor(() =>
      expect(clearLocalData).toHaveBeenCalledWith({
        workspace_cache_ids: ["0123456789abcdef"],
      }),
    );
    confirmSpy.mockRestore();
  });

  it("includes independently managed workspace caches in clear all", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const clearLocalData = vi.fn().mockResolvedValue({
      results: sampleStatus().buckets.map((bucket) => ({
        id: bucket.id,
        ok: true,
      })),
      workspace_cache_results: [{ id: "0123456789abcdef", ok: true }],
    });
    const client = {
      getLocalData: vi.fn().mockResolvedValue({
        ...sampleStatus(),
        workspace_caches: [
          {
            id: "0123456789abcdef",
            source_root: "/work/firefox",
            logical_bytes: 30 * 1024 ** 3,
            allocated_bytes: 12 * 1024 ** 3,
            last_used: "2026-08-01T00:00:00Z",
          },
        ],
      }),
      clearLocalData,
    };
    const { getByTestId } = render(() => (
      <CacheSettingsPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("cache-clear-all")).toBeTruthy());
    fireEvent.click(getByTestId("cache-clear-all"));
    await waitFor(() =>
      expect(clearLocalData).toHaveBeenCalledWith({
        buckets: sampleStatus().buckets.map((bucket) => bucket.id),
        workspace_cache_ids: ["0123456789abcdef"],
      }),
    );
    confirmSpy.mockRestore();
  });
});
