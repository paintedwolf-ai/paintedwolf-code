import { assertNamedControl } from "../../../test/controls.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SaveBundleResult } from "../../../settings/system/diagnostics-export.ts";
import { DiagnosticsExportPanel } from "./DiagnosticsExportPanel.tsx";

const saveBundle = vi.fn<() => Promise<SaveBundleResult>>();
vi.mock("../../../platform/connection/backend.ts", () => ({
  getBackendConnection: () => ({ baseUrl: "http://127.0.0.1:9058", apiToken: "fixture" }),
}));
vi.mock("../../../settings/system/diagnostics-export.ts", () => ({
  saveDiagnosticsBundle: () => saveBundle(),
}));

describe("diagnostics export feedback", () => {
  beforeEach(() => saveBundle.mockReset());

  it.each(["diagnostics-20260913.zip", "/Users/fixture/Reports café/diagnostics.zip"])(
    "shows the saved destination %s",
    async (path) => {
      saveBundle.mockResolvedValue({ kind: "saved", path });
      render(() => <DiagnosticsExportPanel />);
      fireEvent.click(assertNamedControl(screen.getByTestId("diagnostics-export-save"), "button"));
      await waitFor(() => expect(screen.getByTestId("diagnostics-export-status").textContent).toContain(path));
      expect(saveBundle).toHaveBeenCalledOnce();
    },
  );

  it.each<SaveBundleResult>([{ kind: "cancelled" }, { kind: "failed", detail: "offline" }])(
    "clears the previous destination when a retry is $kind",
    async (result) => {
      saveBundle.mockResolvedValueOnce({ kind: "saved", path: "previous.zip" }).mockResolvedValueOnce(result);
      render(() => <DiagnosticsExportPanel />);
      fireEvent.click(assertNamedControl(screen.getByTestId("diagnostics-export-save"), "button"));
      await waitFor(() => expect(screen.getByTestId("diagnostics-export-status").textContent).toContain("previous.zip"));
      fireEvent.click(assertNamedControl(screen.getByTestId("diagnostics-export-save"), "button"));
      await waitFor(() => expect(assertNamedControl(screen.getByTestId("diagnostics-export-save"), "button").hasAttribute("disabled")).toBe(false));
      if (result.kind === "failed") expect(screen.getByTestId("diagnostics-export-status").textContent).toContain(result.detail);
      else expect(screen.queryByTestId("diagnostics-export-status")).toBeNull();
    },
  );
});
