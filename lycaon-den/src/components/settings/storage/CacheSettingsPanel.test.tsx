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
    });
    const client = {
      getLocalData: vi
        .fn()
        .mockResolvedValueOnce(sampleStatus())
        .mockResolvedValueOnce({
          buckets: sampleStatus().buckets.map((b) =>
            b.id === "web_index" ? { ...b, present: false, bytes: 0 } : b,
          ),
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
});
