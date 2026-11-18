import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LocalGenerationStatus, LocalModelTier } from "../../../api/types.ts";
import { nativePathDialog } from "../../../platform/files/native-path-dialog.ts";
import { includedProvider } from "../../../test/local-generation-fixture.ts";
import { LocalModelSelector } from "./LocalModelSelector.tsx";

vi.mock("../../../platform/runtime.ts", () => ({isTauriRuntime: () => true}));
vi.mock("../../../platform/connection/host-identity.ts", () => ({hostSharesDevice: () => true}));
vi.mock("../../../platform/files/native-path-dialog.ts", () => ({ nativePathDialog: vi.fn() }));
afterEach(() => { cleanup(); vi.clearAllMocks(); });

const tiers: LocalModelTier[] = ["Small", "Medium", "Large"].map((display_name, i) => ({
  id: display_name.toLowerCase(), display_name, eligible: i < 2, recommended: i === 1,
  download_bytes: 2 ** 30, min_memory_bytes: 8 * 2 ** 30, download_available: true,
}));
const initial: LocalGenerationStatus = { state: "idle", recovery_action: "none", available_models: tiers };
const installed: LocalGenerationStatus = { ...initial, selected_model: "medium" };
const progress: LocalGenerationStatus = { ...initial, state: "downloading", target_model: "medium", total_bytes: 200, downloaded_bytes: 100 };

function mount(status = initial) {
  const client = {
    getLocalModelStatus: vi.fn().mockResolvedValue(installed),
    selectLocalModel: vi.fn().mockResolvedValue(installed),
    removeLocalModel: vi.fn().mockResolvedValue({ ...initial, state: "opted_out" }),
    cancelLocalModelTransfer: vi.fn().mockResolvedValue(initial),
  };
  const done = vi.fn();
  render(() => <LocalModelSelector client={client as never} provider={includedProvider(status)}
    onRefresh={async () => undefined} onContinue={done} />);
  return { client, done };
}

describe("Local model selection", () => {
  it("preselects the host recommendation before consent and disables ineligible tiers", () => {
    const { client } = mount();
    expect((screen.getByRole("radio", { name: /Medium/ }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("radio", { name: /Large/ }) as HTMLInputElement).disabled).toBe(true);
    expect(client.selectLocalModel).not.toHaveBeenCalled();
  });

  it("waits for the accepted transfer to finish before advancing", async () => {
    const { client, done } = mount();
    client.selectLocalModel.mockResolvedValue(progress);
    fireEvent.click(screen.getByRole("button", { name: "Download and continue" }));
    expect((await screen.findByRole("progressbar")).getAttribute("value")).toBe("100");
    expect(done).not.toHaveBeenCalled();
    await waitFor(() => expect(done).toHaveBeenCalledOnce(), { timeout: 3000 });
    expect(client.selectLocalModel).toHaveBeenCalledWith("nativegen", { model: "medium", bundle_path: undefined });
  });

  it("cancels without opting out or advancing", async () => {
    const { client, done } = mount();
    client.selectLocalModel.mockResolvedValue(progress);
    fireEvent.click(screen.getByRole("button", { name: "Download and continue" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel transfer" }));
    await waitFor(() => expect(client.cancelLocalModelTransfer).toHaveBeenCalledWith("nativegen"));
    expect(client.removeLocalModel).not.toHaveBeenCalled();
    expect(done).not.toHaveBeenCalled();
  });

  it("allows signed offline imports when the tier has no download", async () => {
    const { client, done } = mount({ ...initial, available_models: tiers.map(t => ({ ...t, download_available: false })) });
    expect((screen.getByRole("button", { name: "Download and continue" }) as HTMLButtonElement).disabled).toBe(true);
    vi.mocked(nativePathDialog).mockResolvedValue({ path: "/models/test.pwmodel", grant: null });
    fireEvent.click(screen.getByText("Import offline bundle"));
    fireEvent.click(screen.getByRole("button", { name: "Choose archive…" }));
    await waitFor(() => expect(done).toHaveBeenCalledOnce());
    expect(client.selectLocalModel).toHaveBeenCalledWith("nativegen", { model: "medium", bundle_path: "/models/test.pwmodel" });
  });

  it("offers a user-initiated seed import", async () => {
    const { client } = mount({ ...initial, seed_available: true });
    fireEvent.click(screen.getByRole("button", { name: "Use packaged bundle" }));
    await waitFor(() => expect(client.selectLocalModel).toHaveBeenCalledWith("nativegen", { model: "medium", bundle_path: ":seed:" }));
  });

  it("offers opt-out without downloading and keeps errors actionable", async () => {
    const { client, done } = mount(installed);
    fireEvent.click(screen.getByRole("radio", { name: "Use no local model" }));
    const remove = screen.getByRole("button", { name: "Remove local model" });
    client.removeLocalModel.mockRejectedValueOnce(new Error("Device busy"));
    fireEvent.click(remove);
    expect((await screen.findByRole("alert")).textContent).toBe("Device busy");
    expect(done).not.toHaveBeenCalled();
    fireEvent.click(remove);
    await waitFor(() => expect(done).toHaveBeenCalledOnce());
    expect(client.selectLocalModel).not.toHaveBeenCalled();
  });
});
