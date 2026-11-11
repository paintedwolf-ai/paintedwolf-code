import type { LocalGenerationStatus, ProviderMeta } from "../../api/types.ts";

const STATES = {
  idle: { label: "Not loaded", detail: "Load the included model to use it on this device." },
  loading: { label: "Loading", detail: "The included model is loading." },
  ready: { label: "Ready", detail: "The included model is ready on this device." },
  repairing: { label: "Repairing", detail: "The included model is being restored from its bundled copy." },
  unavailable: { label: "Unavailable", detail: "The included model could not start." },
  repair_required: { label: "Repair needed", detail: "The installed model needs repair." },
  insufficient_memory: { label: "Not enough memory", detail: "There is not enough memory to load the included model." },
  stopped: { label: "Stopped", detail: "The local model service has stopped." },
} satisfies Record<LocalGenerationStatus["state"], { label: string; detail: string }>;

export function localGenerationView(provider: Pick<ProviderMeta, "local_runtime">) {
  const status = provider.local_runtime;
  const state = status?.state ?? "unavailable";
  const action = status?.recovery_action ?? "none";
  return {
    ...STATES[state],
    ready: state === "ready",
    action,
    actionLabel: action === "repair" ? "Repair model" : state === "idle" ? "Load model" : "Try again",
    reinstall: action === "reinstall",
    busy: state === "loading" || state === "repairing",
  };
}
