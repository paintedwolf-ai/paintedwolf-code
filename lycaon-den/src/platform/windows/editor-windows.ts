import type { EditorParticipant } from "../../api/types.ts";
import { focusItemWindow, type ItemWindowView } from "./item-windows.ts";

export type EditorWindow = { clientId: string; label: string; slot: number; nativeLabel: string | null; title: string };

/** Native window numbers match the title bar and every window menu. */
export function editorWindow(participant: Pick<EditorParticipant, "client_id" | "window_number">,
  views: readonly ItemWindowView[]): EditorWindow | null {
  if (participant.client_id === "window:main") {
    return { clientId: participant.client_id, label: "Main window", slot: 0, nativeLabel: "main", title: "" };
  }
  const view = views.find(entry => `window:${entry.label}` === participant.client_id);
  if (view) return { clientId: participant.client_id, label: `Window ${view.viewNumber}`, slot: view.viewNumber,
    nativeLabel: view.label, title: view.title };
  const number = participant.window_number;
  if (participant.client_id.startsWith("window:")) return null;
  return { clientId: participant.client_id, label: number ? `Browser window ${number}` : "Connecting window",
    slot: number ?? 0, nativeLabel: null, title: "" };
}

export async function focusEditorWindow(window: EditorWindow): Promise<boolean> {
  return window.nativeLabel !== null && await focusItemWindow(window.nativeLabel);
}
