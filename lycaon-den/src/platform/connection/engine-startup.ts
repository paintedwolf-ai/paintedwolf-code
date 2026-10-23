import { createSignal } from "solid-js";
import { listenHostEvent } from "../windows/window-channel.ts";

const ENGINE_STARTUP_PROTOCOL = 1;

const ENGINE_STARTUP_PHASES = [
  "launch",
  "observability",
  "store",
  "upgrade_snapshot",
  "schema_upgrade",
  "upgrade_validation",
  "configuration",
  "user_path",
  "credentials",
  "host_resources",
  "providers",
  "pricing",
  "tools",
  "agents",
  "sessions",
  "policy",
  "events",
  "workflows",
  "workers",
  "scan",
  "research",
  "grounding",
  "coordinator",
  "server",
  "services",
  "recovery",
  "background_work",
  "binding",
] as const;

type EngineStartupPhase = (typeof ENGINE_STARTUP_PHASES)[number];
type EngineStartupStatus = "starting" | "stalled";

type EngineStartupProgress = {
  protocol: number;
  status: EngineStartupStatus;
  phase: EngineStartupPhase;
  pid: number;
  sequence: number;
  elapsed_ms: number;
  silence_ms: number;
};

type EngineStartupState =
  | { status: "idle" }
  | EngineStartupProgress;

const PHASE_LABELS: Record<EngineStartupPhase, string> = {
  launch: "Starting the engine",
  observability: "Preparing diagnostics",
  store: "Opening local data",
  upgrade_snapshot: "Saving recovery data before updating",
  schema_upgrade: "Updating local data",
  upgrade_validation: "Verifying updated local data",
  configuration: "Loading configuration",
  user_path: "Finding command-line tools",
  credentials: "Loading credentials",
  host_resources: "Checking host resources",
  providers: "Loading AI providers",
  pricing: "Loading model pricing",
  tools: "Preparing tools",
  agents: "Loading agents",
  sessions: "Restoring sessions",
  policy: "Loading safety policy",
  events: "Restoring activity",
  workflows: "Loading workflows",
  workers: "Preparing workers",
  scan: "Preparing code scanning",
  research: "Preparing project research",
  grounding: "Preparing grounding",
  coordinator: "Preparing coordination",
  server: "Preparing the local service",
  services: "Connecting local services",
  recovery: "Checking unfinished work",
  background_work: "Starting background work",
  binding: "Opening the local connection",
};

const phaseSet = new Set<string>(ENGINE_STARTUP_PHASES);
const [engineStartupState, setEngineStartupState] =
  createSignal<EngineStartupState>({ status: "idle" });

export { engineStartupState };

export function engineStartupPhaseLabel(phase: EngineStartupPhase): string {
  return PHASE_LABELS[phase];
}

function validProgress(value: unknown): value is EngineStartupProgress {
  if (!value || typeof value !== "object") return false;
  const progress = value as Partial<EngineStartupProgress>;
  const hasEnvelope =
    progress.protocol === ENGINE_STARTUP_PROTOCOL &&
    (progress.status === "starting" || progress.status === "stalled") &&
    typeof progress.phase === "string" &&
    phaseSet.has(progress.phase) &&
    typeof progress.pid === "number" &&
    Number.isInteger(progress.pid) &&
    progress.pid > 0 &&
    typeof progress.sequence === "number" &&
    Number.isInteger(progress.sequence) &&
    progress.sequence >= 0 &&
    typeof progress.elapsed_ms === "number" &&
    Number.isInteger(progress.elapsed_ms) &&
    progress.elapsed_ms >= 0 &&
    typeof progress.silence_ms === "number" &&
    Number.isInteger(progress.silence_ms) &&
    progress.silence_ms >= 0;
  if (!hasEnvelope) return false;
  const complete = progress as EngineStartupProgress;
  return complete.status === "stalled"
    ? complete.silence_ms > 0
    : complete.silence_ms === 0;
}

/** Apply one host-validated progress event, ignoring stale attempts and records. */
export function noteEngineStartupProgress(value: unknown): void {
  if (!validProgress(value)) return;
  const current = engineStartupState();
  if (current.status !== "idle") {
    if (current.pid !== value.pid || value.sequence < current.sequence) return;
    if (value.sequence === current.sequence && value.phase !== current.phase) return;
  }
  setEngineStartupState(value);
}

export function clearEngineStartupProgress(): void {
  setEngineStartupState({ status: "idle" });
}

/** Subscribe before invoking start so the first child record cannot race the UI. */
export async function beginEngineStartupObservation(): Promise<() => void> {
  clearEngineStartupProgress();
  return listenHostEvent<EngineStartupProgress>("sidecar-startup", ({ payload }) => {
    noteEngineStartupProgress(payload);
  });
}
