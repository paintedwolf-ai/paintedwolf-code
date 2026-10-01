import { stubClient } from "../../../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { PowerSettingsPanel } from "./PowerSettingsPanel.tsx";

describe("PowerSettingsPanel", () => {
  it("loads the default-on setting and persists changes", async () => {
    const updatePowerSettings = vi.fn().mockResolvedValue({
      keep_awake_while_working: false,
      supported: true,
      inhibiting: false,
      active_work_count: 0,
    });
    const client = stubClient({
      getPowerSettings: vi.fn().mockResolvedValue({
        keep_awake_while_working: true,
        supported: true,
        inhibiting: false,
        active_work_count: 0,
      }),
      updatePowerSettings,
    });

    render(() => <PowerSettingsPanel client={client} />);
    const checkbox = await screen.findByTestId<HTMLInputElement>("power-keep-awake");
    await waitFor(() => {
      expect(checkbox.checked).toBe(true);
      expect(checkbox.disabled).toBe(false);
    });
    fireEvent.click(checkbox);
    await waitFor(() => {
      expect(updatePowerSettings).toHaveBeenCalledWith({
        keep_awake_while_working: false,
      });
    });
  });
});
