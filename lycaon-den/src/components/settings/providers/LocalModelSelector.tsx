import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { SettingsClient } from "../../../api/http-capabilities/settings.ts";
import type { LocalGenerationStatus, ProviderMeta } from "../../../api/types.ts";
import { hostSharesDevice } from "../../../platform/connection/host-identity.ts";
import { OfflineModelImport } from "./OfflineModelImport.tsx";
import { DenRadio } from "../../primitives/DenRadio.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";

type Props = {
  client: SettingsClient;
  provider: ProviderMeta;
  onRefresh: () => Promise<void>;
  onContinue?: () => void;
};

const NONE = "";
const gibibytes = (bytes: number) =>
  `${(bytes / 2 ** 30).toLocaleString(undefined, { maximumFractionDigits: 2 })} GiB`;

export function LocalModelSelector(props: Props) {
  const [snapshot, setSnapshot] = createSignal<LocalGenerationStatus>();
  const [choice, setChoice] = createSignal<string>();
  const [working, setWorking] = createSignal(false);
  const [error, setError] = createSignal("");
  const [pendingModel, setPendingModel] = createSignal<string>();
  const status = () => snapshot() ?? props.provider.local_runtime;
  const tiers = () => status()?.available_models ?? [];
  const selected = () => choice() ?? status()?.selected_model
    ?? (status()?.state === "opted_out" ? NONE : tiers().find(t => t.eligible && t.recommended)?.id ?? NONE);
  const tier = () => tiers().find(t => t.id === selected());
  const seedTier = () => status()?.seed_available ? tiers().find(t => t.id === status()?.seed_model) : undefined;
  const downloading = () => status()?.state === "downloading";
  const current = () => !!selected() && selected() === status()?.selected_model;
  const canApply = () => {
    if (working()) return false;
    if (current() || (!selected() && status()?.state === "opted_out")) return !!props.onContinue;
    return !selected() || (tier()?.eligible && tier()?.download_available);
  };
  const applyLabel = () => {
    if (current()) return props.onContinue ? "Continue with installed model" : "Installed";
    if (selected()) return props.onContinue ? "Download and continue" : "Download model";
    if (status()?.selected_model) return "Remove local model";
    return props.onContinue ? "Continue without a local model" : "Use no local model";
  };
  let alive = true;
  let version = 0;
  onCleanup(() => { alive = false; version++; });

  const accept = async (next: LocalGenerationStatus) => {
    if (!alive) return;
    setSnapshot(next);
    if (next.state === "downloading") return;
    const target = pendingModel();
    setPendingModel(undefined);
    await props.onRefresh();
    if (!alive) return;
    setSnapshot(undefined);
    const selectedTarget = next.selected_model === target && (next.state === "ready" || next.state === "idle");
    if (target !== undefined && selectedTarget) {
      props.onContinue?.();
    } else if (target !== undefined) {
      setError(next.detail || "The requested model change did not finish.");
    }
  };

  createEffect(() => {
    if (!downloading()) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const ticket = version;
      try {
        const next = await props.client.getLocalModelStatus(props.provider.id);
        if (!stopped && ticket === version && !working()) {
          setError("");
          await accept(next);
        }
      } catch (err) {
        if (!stopped && ticket === version) setError(err instanceof Error ? err.message : String(err));
      }
      if (!stopped) timer = setTimeout(() => void poll(), 750);
    };
    timer = setTimeout(() => void poll(), 750);
    onCleanup(() => { stopped = true; clearTimeout(timer); });
  });

  const run = async (action: "apply" | "import" | "seed" | "cancel", path?: string) => {
    if (working() || !alive) return;
    const seed = seedTier();
    if (action === "seed" && !seed?.eligible) return;
    const model = action === "seed" && seed ? seed.id : selected();
    version++;
    setWorking(true);
    setError("");
    try {
      if (action === "cancel") {
        setPendingModel(undefined);
        await accept(await props.client.cancelLocalModelTransfer(props.provider.id));
        return;
      }
      if (action === "apply" && (current() || (!model && status()?.state === "opted_out"))) {
        props.onContinue?.();
        return;
      }
      if (!model) {
        await props.client.deleteLocalModel(props.provider.id);
        await props.onRefresh();
        if (alive) props.onContinue?.();
        return;
      }
      setPendingModel(model);
      const selection = action === "seed" ? { model, packaged_seed: true } : { model, bundle_path: path };
      await accept(await props.client.selectLocalModel(props.provider.id, selection));
    } catch (err) {
      if (alive) { setPendingModel(undefined); setError(err instanceof Error ? err.message : String(err)); }
    } finally {
      if (alive) setWorking(false);
    }
  };

  return (
    <div class="onboarding-gate__card--stack">
      <fieldset class="m-0 grid gap-2 border-0 p-0" disabled={working() || downloading()}>
        <legend class="sr-only">Local model for this device</legend>
        <For each={tiers()}>{model => (
          <DenRadio class="items-baseline py-1" name="local-model-tier" checked={selected() === model.id}
            disabled={!model.eligible} onChange={() => setChoice(model.id)}>
            <strong>{model.display_name}</strong>
              <Show when={model.recommended}> · Recommended for this device</Show>
              <small class="block text-[var(--den-text-muted)]">{gibibytes(model.min_memory_bytes)} memory required
                <Show when={model.download_bytes > 0}> · {gibibytes(model.download_bytes)} download</Show>
                <Show when={!model.eligible}> · Insufficient device memory</Show>
              </small>
          </DenRadio>
        )}</For>
        <DenRadio class="items-baseline py-1" name="local-model-tier" checked={!selected()} onChange={() => setChoice(NONE)}>
          Use no local model
        </DenRadio>
      </fieldset>
      <Show when={downloading()} fallback={
        <>
          <Show when={tier() && !current() && !tier()?.download_available}>
            <p class="den-settings-hint">A download for this tier is not available yet.</p>
          </Show>
          <Show when={status()?.selected_model && !current()}>
            <p class="den-settings-hint">{selected() ? "Switching replaces your installed local model." : "Continuing removes your installed local model."}</p>
          </Show>
          <Show when={tier()?.eligible && hostSharesDevice()}>
            <OfflineModelImport busy={working()} onImport={path => run("import", path)} onFailure={setError} />
          </Show>
          <div class="den-settings-provider-actions">
            <Show when={seedTier()} keyed>{seed => (
              <DenButton variant="secondary" disabled={working() || !seed.eligible}
                onClick={() => void run("seed")}>Install packaged {seed.display_name}</DenButton>
            )}</Show>
            <DenButton variant="primary" disabled={!canApply()} onClick={() => void run("apply")}>
              {applyLabel()}
            </DenButton>
          </div>
        </>
      }>
        <p role="status">Installing {tiers().find(t => t.id === status()?.target_model)?.display_name ?? "local model"}…</p>
        <progress aria-label="Model transfer" value={status()?.downloaded_bytes ?? 0} max={status()?.total_bytes || 1} />
        <small>{gibibytes(status()?.downloaded_bytes ?? 0)} of {gibibytes(status()?.total_bytes ?? 0)}</small>
        <DenButton variant="secondary" disabled={working()} onClick={() => void run("cancel")}>Cancel transfer</DenButton>
      </Show>
      <Show when={error()}><p role="alert" class="den-settings-warn">{error()}</p></Show>
    </div>
  );
}
