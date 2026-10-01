// Host events carry peer-view broadcasts across workspaces.
import { isTauriRuntime } from "../runtime.ts";

type Unlisten = () => void;

/** Subscribe to a host-managed event. Inert outside the desktop host. */
export async function listenHostEvent<T>(
  event: string,
  handler: (event: { payload: T }) => void,
): Promise<Unlisten> {
  if (!isTauriRuntime()) return () => {};
  const { listen } = await import("@tauri-apps/api/event");
  return listen<T>(event, handler);
}
