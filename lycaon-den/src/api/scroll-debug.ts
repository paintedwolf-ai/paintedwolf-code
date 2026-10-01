const endpoint = "/__den/scroll-debug";

export async function sendScrollDebugBatch(body: string, signal: AbortSignal): Promise<boolean> {
  const response = await fetch(endpoint, {
    method: "POST", headers: { "Content-Type": "application/json" }, body, signal,
  });
  return response.ok;
}

export function flushScrollDebugBatch(body: string): boolean {
  return navigator.sendBeacon?.(endpoint, new Blob([body], { type: "application/json" })) ?? false;
}
