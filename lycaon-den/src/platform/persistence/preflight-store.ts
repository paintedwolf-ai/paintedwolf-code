import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { getLycaonClient } from "../connection/app-connection.ts";
import { setPreflightReport } from "./preflight-report.ts";

const [refreshing, setRefreshing] = createSignal(false);

export type PreflightRefreshOutcome = "refreshed" | "unavailable";

let inFlight: { client: LycaonClient; promise: Promise<PreflightRefreshOutcome> } | null = null;

export function preflightRefreshing(): boolean {
  return refreshing();
}

/** Concurrent reads from the same backend share a request. */
export function refreshPreflight(): Promise<PreflightRefreshOutcome> {
  const client = getLycaonClient();
  if (!client) return Promise.resolve("unavailable");
  if (inFlight?.client === client) return inFlight.promise;

  setRefreshing(true);
  const flight = { client, promise: Promise.resolve<PreflightRefreshOutcome>("unavailable") };
  flight.promise = (async () => client.getPreflight())()
    .then((next) => {
      if (inFlight !== flight || getLycaonClient() !== client) return "unavailable" as const;
      setPreflightReport(next);
      return "refreshed" as const;
    })
    .catch(() => "unavailable" as const)
    .finally(() => {
      if (inFlight !== flight) return;
      setRefreshing(false);
      inFlight = null;
    });
  inFlight = flight;
  return flight.promise;
}

export function resetPreflightStore(): void {
  inFlight = null;
  setPreflightReport(undefined);
  setRefreshing(false);
}
