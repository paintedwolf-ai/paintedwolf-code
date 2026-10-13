import { stubClient } from "../../../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { ResidentPresenceProvider } from "../../../ui/resident-presence-context.tsx";
import { resetSurfaceQueriesForTests } from "../../../ui/surface-query.ts";
import { PowerSettingsPanel } from "./PowerSettingsPanel.tsx";

describe("PowerSettingsPanel", () => {
  afterEach(() => {
    vi.useRealTimers();
    resetSurfaceQueriesForTests();
  });

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
  it("refreshes service loss and recovery only while resident", async () => {
    vi.useFakeTimers();
    let facts = { keep_awake_while_working: true, supported: true, inhibiting: true, active_work_count: 1 };
    const read = vi.fn(async () => ({ ...facts }));
    const client = stubClient({ getPowerSettings: read });
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    const view = render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <PowerSettingsPanel client={client} />
      </ResidentPresenceProvider>
    ));
    await vi.advanceTimersByTimeAsync(0);
    expect(read).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/Keeping this .* awake for active work\./)).toBeTruthy();
    facts = { ...facts, supported: false, inhibiting: false };
    await vi.advanceTimersByTimeAsync(5_000);
    expect(screen.queryByText(/Keeping this .* awake for active work\./)).toBeNull();
    expect(screen.getByTestId<HTMLInputElement>("power-keep-awake").disabled).toBe(true);
    setPresence("idle");
    const idleReads = read.mock.calls.length;
    await vi.advanceTimersByTimeAsync(15_000);
    expect(read).toHaveBeenCalledTimes(idleReads);
    facts = { ...facts, supported: true, inhibiting: true };
    setPresence("active");
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByTestId<HTMLInputElement>("power-keep-awake").disabled).toBe(false);
    expect(screen.getByText(/Keeping this .* awake for active work\./)).toBeTruthy();
    view.unmount();
    const disposedReads = read.mock.calls.length;
    await vi.advanceTimersByTimeAsync(15_000);
    expect(read).toHaveBeenCalledTimes(disposedReads);
  });

  it("does not overlap pending reads or preference writes", async () => {
    vi.useFakeTimers();
    const facts = { keep_awake_while_working: true, supported: true, inhibiting: false, active_work_count: 0 };
    let finishRead!: (value: typeof facts) => void;
    let finishWrite!: (value: typeof facts) => void;
    const read = vi.fn().mockResolvedValueOnce(facts).mockImplementationOnce(() =>
      new Promise<typeof facts>((resolve) => { finishRead = resolve; })).mockResolvedValue({ ...facts, keep_awake_while_working: false });
    const write = vi.fn(() => new Promise<typeof facts>((resolve) => { finishWrite = resolve; }));
    render(() => <PowerSettingsPanel client={stubClient({ getPowerSettings: read, updatePowerSettings: write })} />);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(read).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId<HTMLInputElement>("power-keep-awake").disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(read).toHaveBeenCalledTimes(2);
    finishRead(facts);
    await vi.advanceTimersByTimeAsync(0);
    fireEvent.click(screen.getByTestId("power-keep-awake"));
    await vi.advanceTimersByTimeAsync(10_000);
    expect(write).toHaveBeenCalledTimes(1);
    expect(read).toHaveBeenCalledTimes(2);
    finishWrite({ ...facts, keep_awake_while_working: false });
    await vi.advanceTimersByTimeAsync(5_000);
    expect(read).toHaveBeenCalledTimes(3);
  });

});
