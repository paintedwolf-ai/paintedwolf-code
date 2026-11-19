import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { SettingsClient } from "../../../api/http-capabilities/settings.ts";
import type { LocalGenerationStatus, LocalModelSelectionRequest, ProviderMeta } from "../../../api/types.ts";
import { hostSharesDevice } from "../../../platform/connection/host-identity.ts";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { LocalModelChoices } from "./LocalModelChoices.tsx";
import { LocalModelTransfer } from "./LocalModelTransfer.tsx";
import { OfflineModelImport } from "./OfflineModelImport.tsx";
import { formatModelBytes } from "./model-size.ts";

type Props = {
  client: SettingsClient;
  provider: ProviderMeta;
  onRefresh: () => Promise<void>;
  onContinue?: () => void;
};

function usable(status?: LocalGenerationStatus) {
  return status?.state === "ready" || status?.state === "idle";
}

export function LocalModelSelector(props: Props) {
  const [snapshot, setSnapshot] = createSignal<LocalGenerationStatus>();
  const [choice, setChoice] = createSignal<string>();
  const [working, setWorking] = createSignal(false);
  const [error, setError] = createSignal("");
  const [retry, setRetry] = createSignal<LocalModelSelectionRequest>();
  let pending: LocalModelSelectionRequest | undefined;
  let version = 0;
  let alive = true;
  onCleanup(() => { alive = false; version++; });

  const status = () => snapshot() ?? props.provider.local_runtime;
  const tiers = () => status()?.available_models ?? [];
  const suggested = () => {
    const recommended = tiers().find(t => t.eligible && t.recommended);
    if (props.onContinue && !recommended?.download_available) {
      const published = tiers().find(t => t.eligible && t.download_available);
      if (published) return published.id;
    }
    return recommended?.id ?? "";
  };
  const selected = () => choice() ?? status()?.selected_model
    ?? (status()?.state === "opted_out" ? "" : suggested());
  const tier = () => tiers().find(t => t.id === selected());
  const seedTier = () => status()?.seed_available ? tiers().find(t => t.id === status()?.seed_model) : undefined;
  const transfer = () => status()?.state === "downloading" ? status() : undefined;
  const installed = () => !!selected() && selected() === status()?.selected_model;
  const current = () => installed() && usable(status());
  const retryImport = () => !!(retry()?.bundle_path || retry()?.packaged_seed);
  const canApply = () => {
    if (working()) return false;
    if (retryImport()) return !!tier()?.eligible;
    if (current() || (!selected() && status()?.state === "opted_out")) return !!props.onContinue;
    return !selected() || (tier()?.eligible && tier()?.download_available);
  };
  const applyLabel = () => {
    if (retryImport()) return "Retry import";
    if (current()) return props.onContinue ? "Continue with installed model" : "Installed";
    if (selected()) {
      if (!tier()?.download_available) return "Download unavailable";
      const size = tier()?.download_bytes;
      const action = retry() ? "Retry download" : "Download";
      return action + (size ? ` ${formatModelBytes(size)}` : "") + (props.onContinue ? " and continue" : "");
    }
    if (status()?.selected_model) return "Remove local model";
    return props.onContinue ? "Continue without a local model" : "Use no local model";
  };

  const choose = (id: string) => { setChoice(id); setError(""); setRetry(undefined); };

  const accept = async (next: LocalGenerationStatus, ticket: number) => {
    if (!alive || version !== ticket) return;
    setSnapshot(next);
    if (next.state === "downloading") return;
    const request = pending;
    pending = undefined;
    setWorking(true);
    try {
      await props.onRefresh();
      if (!alive || version !== ticket) return;
      // Retain the confirmed result until the provider list catches up.
      const refreshed = props.provider.local_runtime;
      if (refreshed?.state === next.state && refreshed.selected_model === next.selected_model) setSnapshot(undefined);
      if (!request) return;
      if (next.selected_model === request.model && usable(next)) {
        setRetry(undefined);
        setChoice(undefined);
        props.onContinue?.();
      } else {
        setRetry(request);
        setError(next.detail || "Installation did not finish. Try again.");
      }
    } finally {
      if (alive && version === ticket) setWorking(false);
    }
  };

  createEffect(() => {
    if (!transfer()) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const ticket = version;
      try {
        const next = await props.client.getLocalModelStatus(props.provider.id);
        if (!stopped && ticket === version && !working()) {
          setError("");
          await accept(next, ticket);
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
    const ticket = ++version;
    setWorking(true);
    setError("");
    try {
      if (action === "cancel") {
        const interrupted = pending;
        const next = await props.client.cancelLocalModelTransfer(props.provider.id);
        pending = undefined;
        if (next.state !== "downloading") {
          setRetry(interrupted);
          setChoice(interrupted?.model);
        }
        await accept(next, ticket);
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
      pending = action === "seed" ? { model, packaged_seed: true }
        : action === "import" ? { model, bundle_path: path } : retry() ?? { model };
      choose(model);
      await accept(await props.client.selectLocalModel(props.provider.id, pending), ticket);
    } catch (err) {
      if (alive) {
        if (action !== "cancel" && pending) { setRetry(pending); pending = undefined; }
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (alive && version === ticket) setWorking(false);
    }
  };

  return (
    <div class="onboarding-gate__card--stack">
      <ShowLatest when={transfer()} fallback={
        <>
          <LocalModelChoices tiers={tiers()} selected={selected()} installed={status()?.selected_model} busy={working()} onChoose={choose} />
          <Show when={status()?.selected_model && !installed()}>
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
            <DenButton variant="primary" disabled={!canApply()} onClick={() => void run("apply")}>{applyLabel()}</DenButton>
          </div>
        </>
      }>{active =>
        <LocalModelTransfer status={active()} busy={working()} onCancel={() => void run("cancel")} />
      }</ShowLatest>
      <Show when={error()}><p role="alert" class="den-settings-warn">{error()}</p></Show>
    </div>
  );
}
