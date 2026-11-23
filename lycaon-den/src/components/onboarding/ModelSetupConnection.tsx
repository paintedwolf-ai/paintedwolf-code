import { Show, createSignal } from "solid-js";
import { DenButton } from "../primitives/DenButton.tsx";

type Props = {
  connected: boolean;
  checked: boolean;
  onRetry: () => Promise<void>;
  onContinue: () => void;
};

export function ModelSetupConnection(props: Props) {
  const [checking, setChecking] = createSignal(false);
  const [error, setError] = createSignal("");
  const check = async () => {
    setChecking(true);
    setError("");
    try {
      await props.onRetry();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setChecking(false);
    }
  };
  return (
    <div class="onboarding-gate__card--stack">
      <p role="status">{error() || (!props.connected ? "Waiting for the local engine…"
        : !props.checked || checking() ? "Checking the installation…" : "Model availability could not be confirmed.")}</p>
      <div class="den-settings-provider-actions">
        <Show when={props.connected}>
          <DenButton variant="secondary" disabled={checking()} onClick={() => void check()}>Check again</DenButton>
        </Show>
        <DenButton variant="secondary" onClick={props.onContinue}>Continue without configuring a model</DenButton>
      </div>
    </div>
  );
}
