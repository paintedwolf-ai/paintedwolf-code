import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LocalGenerationStatus, LocalModelTier } from "../../../api/types.ts";
import { isTauriRuntime } from "../../../platform/runtime.ts";
import { hostSharesDevice } from "../../../platform/connection/host-identity.ts";
import { nativePathDialog } from "../../../platform/files/native-path-dialog.ts";
import { includedProvider } from "../../../test/local-generation-fixture.ts";
import { LocalModelSelector } from "./LocalModelSelector.tsx";

vi.mock("../../../platform/runtime.ts", () => ({isTauriRuntime: vi.fn(() => true)}));
vi.mock("../../../platform/connection/host-identity.ts", () => ({hostSharesDevice: vi.fn(() => true)}));
vi.mock("../../../platform/files/native-path-dialog.ts", () => ({ nativePathDialog: vi.fn() }));
afterEach(() => { cleanup(); vi.clearAllMocks(); vi.mocked(hostSharesDevice).mockReturnValue(true); vi.mocked(isTauriRuntime).mockReturnValue(true); });

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
    deleteLocalModel: vi.fn().mockResolvedValue(undefined),
    cancelLocalModelTransfer: vi.fn().mockResolvedValue(initial),
  };
  const done = vi.fn();
  const refresh = vi.fn().mockResolvedValue(undefined);
  render(() => <LocalModelSelector client={client as never} provider={includedProvider(status)}
    onRefresh={refresh} onContinue={done} />);
  return { client, done, refresh };
}

describe("Local model selection", () => {
  it("preselects the host recommendation before consent and disables ineligible tiers", () => {
    const { client } = mount();
    expect((screen.getByRole("radio", { name: /Medium/ }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("radio", { name: /Large/ }) as HTMLInputElement).disabled).toBe(true);
    expect(client.selectLocalModel).not.toHaveBeenCalled();
  });

  it("preselects an eligible download when the recommended tier is unpublished", () => {
    const partial = {...initial, available_models: tiers.map(t => ({...t, download_available: !t.recommended}))};
    const {client} = mount(partial);
    const small = screen.getByRole("radio", {name: /Small/}) as HTMLInputElement;
    const medium = screen.getByRole("radio", {name: /Medium/}) as HTMLInputElement;
    expect(small.checked).toBe(true);
    fireEvent.click(medium);
    expect(medium.checked).toBe(true);
    expect(screen.getByText("Import offline bundle")).toBeTruthy();
    expect(client.selectLocalModel).not.toHaveBeenCalled();
    cleanup();
    mount({...partial, selected_model: "medium"});
    expect((screen.getByRole("radio", {name: /Medium/}) as HTMLInputElement).checked).toBe(true);
    cleanup();
    mount({...partial, state: "opted_out"});
    expect((screen.getByRole("radio", {name: "Use no local model"}) as HTMLInputElement).checked).toBe(true);
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

  it("keeps setup open when installation finishes but the model cannot load", async () => {
    const { client, done } = mount();
    client.selectLocalModel.mockResolvedValue({ ...installed, state: "unavailable", detail: "The runtime could not load the model." });
    fireEvent.click(screen.getByRole("button", { name: "Download and continue" }));
    expect((await screen.findByRole("alert")).textContent).toBe("The runtime could not load the model.");
    expect(done).not.toHaveBeenCalled();
  });

  it("cancels without opting out or advancing", async () => {
    const { client, done } = mount();
    client.selectLocalModel.mockResolvedValue(progress);
    fireEvent.click(screen.getByRole("button", { name: "Download and continue" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel transfer" }));
    await waitFor(() => expect(client.cancelLocalModelTransfer).toHaveBeenCalledWith("nativegen"));
    expect(client.deleteLocalModel).not.toHaveBeenCalled();
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

  it("accepts a bundle folder on a shared device in a browser", async () => {
    vi.mocked(isTauriRuntime).mockReturnValue(false);
    const { client } = mount();
    fireEvent.click(screen.getByText("Import offline bundle"));
    fireEvent.input(screen.getByLabelText("Bundle file or folder"), {target: {value: "/models/medium"}});
    fireEvent.click(screen.getByRole("button", {name: "Import bundle"}));
    await waitFor(() => expect(client.selectLocalModel).toHaveBeenCalledWith("nativegen", { model: "medium", bundle_path: "/models/medium" }));
  });

  it("keeps client-side paths off a remote host", () => {
    vi.mocked(hostSharesDevice).mockReturnValue(false);
    mount();
    expect(screen.queryByText("Import offline bundle")).toBeNull();
  });

  it("imports the packaged tier even when another tier is recommended", async () => {
    const { client } = mount({ ...initial, seed_available: true, seed_model: "small" });
    fireEvent.click(screen.getByRole("button", { name: "Install packaged Small" }));
    await waitFor(() => expect(client.selectLocalModel).toHaveBeenCalledWith("nativegen", { model: "small", packaged_seed: true }));
  });

  it("disables a packaged model which this device cannot run", () => {
    mount({ ...initial, seed_available: true, seed_model: "large" });
    expect((screen.getByRole("button", {name: "Install packaged Large"}) as HTMLButtonElement).disabled).toBe(true);
  });

  it("offers opt-out without downloading and keeps errors actionable", async () => {
    const { client, done, refresh } = mount(installed);
    fireEvent.click(screen.getByRole("radio", { name: "Use no local model" }));
    const remove = screen.getByRole("button", { name: "Remove local model" });
    client.deleteLocalModel.mockRejectedValueOnce(new Error("Device busy"));
    fireEvent.click(remove);
    expect((await screen.findByRole("alert")).textContent).toBe("Device busy");
    expect(done).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
    fireEvent.click(remove);
    await waitFor(() => expect(done).toHaveBeenCalledOnce());
    expect(refresh).toHaveBeenCalledOnce();
    expect(client.selectLocalModel).not.toHaveBeenCalled();
  });
});
