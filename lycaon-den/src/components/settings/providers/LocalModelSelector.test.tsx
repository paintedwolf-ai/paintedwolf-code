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
  id: display_name.toLowerCase(), display_name, description: `Description of ${display_name}`, eligible: i < 2, recommended: i === 1,
  download_bytes: 2 ** 30, min_memory_bytes: 8 * 2 ** 30, download_available: true,
}));
const initial: LocalGenerationStatus = { state: "idle", recovery_action: "none", available_models: tiers };
const installed: LocalGenerationStatus = { ...initial, selected_model: "medium" };
const progress: LocalGenerationStatus = { ...initial, state: "downloading", target_model: "medium", total_bytes: 200, downloaded_bytes: 100 };

function mount(status = initial, setup = true) {
  const client = {
    getLocalModelStatus: vi.fn().mockResolvedValue(installed),
    selectLocalModel: vi.fn().mockResolvedValue(installed),
    deleteLocalModel: vi.fn().mockResolvedValue(undefined),
    cancelLocalModelTransfer: vi.fn().mockResolvedValue(initial),
  };
  const done = vi.fn();
  const refresh = vi.fn().mockResolvedValue(undefined);
  render(() => <LocalModelSelector client={client as never} provider={includedProvider(status)}
    onRefresh={refresh} onContinue={setup ? done : undefined} />);
  return { client, done, refresh };
}

describe("Local model selection", () => {
  it("preselects the host recommendation before consent and disables ineligible tiers", () => {
    const { client } = mount();
    expect((screen.getByRole("radio", { name: /Medium/ }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("radio", { name: /Large/ }) as HTMLInputElement).disabled).toBe(true);
    expect(client.selectLocalModel).not.toHaveBeenCalled();
  });

  it.each([["system", "system memory"], ["gpu", "GPU memory"]] as const)("uses the host's %s memory label", (memory_kind, label) => {
    mount({...initial, memory_kind});
    expect(screen.getByRole("radio", {name: /Medium/}).parentElement?.textContent).toContain(`Requires 8 GiB of ${label}`);
  });

  it("rounds a fractional memory floor upward", () => {
    mount({...initial, memory_kind:"gpu", available_models: tiers.map(t => ({...t, min_memory_bytes:19_885_870_182, download_bytes:14_471_548_048}))});
    expect(screen.getByRole("radio", {name: /Medium/}).parentElement?.textContent).toContain("13 GiB download · Requires 19 GiB of GPU memory");
    expect(screen.getByRole("button", {name:"Download 13 GiB and continue"})).toBeTruthy();
  });

  it("keeps rounded progress distinct from completion and measures every byte", () => {
    mount({...progress, downloaded_bytes: 6_500_000_000, total_bytes:6_731_357_749});
    expect(screen.getByText("About 6 GiB transferred")).toBeTruthy();
    expect(screen.getByText("96%")).toBeTruthy();
    const bar = screen.getByRole("progressbar");
    expect(bar.getAttribute("value")).toBe("6500000000");
    expect(bar.getAttribute("max")).toBe("6731357749");
    cleanup();
    mount({...progress, downloaded_bytes: 1, total_bytes:1048576});
    expect(screen.getByText("1 B of 1 MiB")).toBeTruthy();
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

  it("preserves the saved opt-out when the runtime is unavailable", () => {
    const {client} = mount({...initial, state: "unavailable", selection_state: "opted_out"}, false);
    expect((screen.getByRole("radio", {name: "Use no local model"}) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("button", {name: "Use no local model"}) as HTMLButtonElement).disabled).toBe(true);
    expect(client.deleteLocalModel).not.toHaveBeenCalled();
  });

  it("offers no model by default when the only published tier exceeds device capacity", () => {
    const {client} = mount({...initial, available_models: tiers.map(t => ({...t, download_available: !t.eligible}))});
    expect((screen.getByRole("radio", {name: "Use no local model"}) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("button", {name: "Continue without a local model"}) as HTMLButtonElement).disabled).toBe(false);
    expect(client.selectLocalModel).not.toHaveBeenCalled();
    expect(client.deleteLocalModel).not.toHaveBeenCalled();
  });

  it("waits for the accepted transfer to finish before advancing", async () => {
    const { client, done } = mount();
    client.selectLocalModel.mockResolvedValue(progress);
    fireEvent.click(screen.getByRole("button", { name: "Download 1 GiB and continue" }));
    expect((await screen.findByRole("progressbar")).getAttribute("value")).toBe("100");
    expect(screen.getByText("50%")).toBeTruthy();
    expect(screen.getByText("100 B of 200 B")).toBeTruthy();
    expect(done).not.toHaveBeenCalled();
    await waitFor(() => expect(done).toHaveBeenCalledOnce(), { timeout: 3000 });
    expect(client.selectLocalModel).toHaveBeenCalledWith("nativegen", { model: "medium" });
  });

  it("keeps setup open when installation finishes but the model cannot load", async () => {
    const { client, done } = mount();
    client.selectLocalModel.mockResolvedValue({ ...installed, state: "unavailable", detail: "The runtime could not load the model." });
    fireEvent.click(screen.getByRole("button", { name: "Download 1 GiB and continue" }));
    expect((await screen.findByRole("alert")).textContent).toBe("The runtime could not load the model.");
    expect(done).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", {name: "Continue with installed model"})).toBeNull();
    client.selectLocalModel.mockResolvedValue(installed);
    fireEvent.click(screen.getByRole("button", {name: "Retry download 1 GiB and continue"}));
    await waitFor(() => expect(done).toHaveBeenCalledOnce());
  });

  it("cancels without opting out or advancing", async () => {
    const { client, done } = mount();
    client.selectLocalModel.mockResolvedValue(progress);
    fireEvent.click(screen.getByRole("button", { name: "Download 1 GiB and continue" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel transfer" }));
    await waitFor(() => expect(client.cancelLocalModelTransfer).toHaveBeenCalledWith("nativegen"));
    expect(client.deleteLocalModel).not.toHaveBeenCalled();
    expect(done).not.toHaveBeenCalled();
    expect(await screen.findByRole("button", {name: "Retry download 1 GiB and continue"})).toBeTruthy();
  });

  it("shows an indeterminate import and protects activation from cancellation", async () => {
    mount({...progress, transfer_phase: "importing", total_bytes: 0});
    expect(screen.getByText("Importing Medium…")).toBeTruthy();
    expect(screen.getByText("100 B transferred")).toBeTruthy();
    expect(screen.getByRole("progressbar").hasAttribute("value")).toBe(false);
    cleanup();
    mount({...progress, transfer_phase: "activating"});
    expect(screen.getByText("Activating Medium…")).toBeTruthy();
    expect(screen.queryByRole("button", {name:"Cancel transfer"})).toBeNull();
  });

  it("allows signed offline imports when the tier has no download", async () => {
    const { client, refresh } = mount({ ...initial, available_models: tiers.map(t => ({ ...t, download_available: false })) }, false);
    expect((screen.getByRole("button", { name: "Download unavailable" }) as HTMLButtonElement).disabled).toBe(true);
    vi.mocked(nativePathDialog).mockResolvedValue({ path: "/models/test.pwmodel", grant: null });
    fireEvent.click(screen.getByText("Import offline bundle"));
    fireEvent.click(screen.getByRole("button", { name: "Choose archive…" }));
    await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
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
