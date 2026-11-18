import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { SettingsClient } from "../../../api/http-capabilities/settings.ts";
import type { LocalGenerationStatus, ProviderMeta } from "../../../api/types.ts";
import { nativePathDialog } from "../../../platform/files/native-path-dialog.ts";
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
  const [continueAfter, setContinueAfter] = createSignal<string>();
  const status = () => snapshot() ?? props.provider.local_runtime;
  const tiers = () => status()?.available_models ?? [];
  const selected = () => choice() ?? status()?.selected_model
    ?? (status()?.state === "opted_out" ? NONE : tiers().find(t => t.eligible && t.recommended)?.id ?? NONE);
  const tier = () => tiers().find(t => t.id === selected());
  const downloading = () => status()?.state === "downloading";
  const current = () => !!selected() && selected() === status()?.selected_model;
  let alive = true;
  let version = 0;
  onCleanup(() => { alive = false; version++; });

  const accept = async (next: LocalGenerationStatus) => {
    if (!alive) return;
    setSnapshot(next);
    if (next.state === "downloading") return;
    const target = continueAfter();
    setContinueAfter(undefined);
    await props.onRefresh();
    if (!alive) return;
    setSnapshot(undefined);
    if (target !== undefined && (next.selected_model === target || (target === NONE && next.state === "opted_out"))) {
      props.onContinue?.();
    } else if (target !== undefined && next.detail) {
      setError(next.detail);
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
        if (!stopped && ticket === version) {
          setError("");
          await accept(next);
        }
      } catch (err) {
        if (!stopped && ticket === version) setError(err instanceof Error ? err.message : String(err));
      }
      if (!stopped) timer = setTimeout(poll, 750);
    };
    timer = setTimeout(poll, 750);
    onCleanup(() => { stopped = true; clearTimeout(timer); });
  });

  const run = async (action: "apply" | "import" | "seed" | "cancel") => {
    if (working()) return;
    const model = selected();
    version++;
    setWorking(true);
    setError("");
    try {
      if (action === "cancel") {
        setContinueAfter(undefined);
        await accept(await props.client.cancelLocalModelTransfer(props.provider.id));
        return;
      }
      if (action === "apply" && current()) { props.onContinue?.(); return; }
      if (action === "apply" && !model && !status()?.selected_model && status()?.state === "opted_out") {
        props.onContinue?.();
        return;
      }
      let path: string | undefined;
      if (action === "import") {
        const picked = await nativePathDialog("Import offline model bundle", {
          kind: "file", filter: { name: "Model bundles", extensions: ["pwmodel", "tar"] },
        });
        if (!picked || !alive) return;
        path = picked.path;
      }
      if (action === "seed") path = ":seed:";
      setContinueAfter(model);
      const next = model
        ? await props.client.selectLocalModel(props.provider.id, { model, bundle_path: path })
        : await props.client.removeLocalModel(props.provider.id);
      await accept(next);
    } catch (err) {
      if (alive) { setContinueAfter(undefined); setError(err instanceof Error ? err.message : String(err)); }
    } finally {
      if (alive) setWorking(false);
    }
  };

  return (
    <div class="onboarding-gate__card--stack">
      <fieldset class="local-model-choices" disabled={working() || downloading()}>
        <legend class="sr-only">Local model for this device</legend>
        <For each={tiers()}>{model => (
          <label class="local-model-choice">
            <input type="radio" name="local-model-tier" checked={selected() === model.id}
              disabled={!model.eligible} onChange={() => setChoice(model.id)} />
            <span><strong>{model.display_name}</strong>
              <Show when={model.recommended}> · Recommended for this device</Show>
              <small>{gibibytes(model.min_memory_bytes)} memory required
                <Show when={model.download_bytes > 0}> · {gibibytes(model.download_bytes)} download</Show>
                <Show when={!model.eligible}> · Insufficient device memory</Show>
              </small>
            </span>
          </label>
        )}</For>
        <label class="local-model-choice">
          <input type="radio" name="local-model-tier" checked={!selected()} onChange={() => setChoice(NONE)} />
          <span>Use no local model</span>
        </label>
      </fieldset>
      <Show when={downloading()} fallback={
        <>
          <Show when={tier() && !current() && !tier()?.download_available}>
            <p class="den-settings-hint">A download for this tier is not available yet. Import a signed bundle or continue without a local model.</p>
          </Show>
          <Show when={status()?.selected_model && !current()}>
            <p class="den-settings-hint">{selected() ? "Switching replaces your installed local model." : "Continuing removes your installed local model."}</p>
          </Show>
          <div class="den-settings-provider-actions">
            <Show when={tier()?.eligible}>
              <DenButton variant="secondary" disabled={working()} onClick={() => void run("import")}>Import offline bundle…</DenButton>
              <Show when={status()?.seed_available}>
                <DenButton variant="secondary" disabled={working()} onClick={() => void run("seed")}>Use packaged bundle</DenButton>
              </Show>
            </Show>
            <DenButton variant="primary" disabled={working() || (selected() ? !current() && !(tier()?.eligible && tier()?.download_available) : !props.onContinue && status()?.state === "opted_out") || (current() && !props.onContinue)}
              onClick={() => void run("apply")}>
              {current() ? (props.onContinue ? "Continue with installed model" : "Installed") : !selected()
                ? (status()?.selected_model ? "Remove local model" : "Continue without a local model")
                : (props.onContinue ? "Download and continue" : "Download model")}
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
