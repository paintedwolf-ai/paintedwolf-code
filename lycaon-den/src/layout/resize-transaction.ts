import { batch, createSignal } from "solid-js";
import type { ListPaneAxis } from "../list/list-pane-model.ts";
import type { ResizeSession } from "./resize-session.ts";
import { beginShellLayoutBusy, endShellLayoutBusy } from "../shell/shell-layout-busy.ts";

type ResizeKey =
  | "nav"
  | "split"
  | `pane:${string}:${ListPaneAxis}`
  | `column:${string}:${string}`;

type ResizeTransaction = {
  id: number;
  key: ResizeKey;
  startValue: number;
  previewValue: number;
  movementThreshold: number;
  commit: (value: number) => void;
};

const [resize, setResize] = createSignal<ResizeTransaction | null>(null);
let nextResizeId = 0;

export function columnResizePreview(surface: string): { column: string; width: number } | null {
  const current = resize();
  const prefix = `column:${surface}:`;
  return current?.key.startsWith(prefix)
    ? { column: current.key.slice(prefix.length), width: current.previewValue }
    : null;
}

export function resizePreview(key: ResizeKey): number | null {
  const current = resize();
  return current?.key === key ? current.previewValue : null;
}

function finishResize(id: number, outcome: "commit" | "cancel"): void {
  const current = resize();
  if (!current || current.id !== id) return;
  try {
    batch(() => {
      setResize(null);
      if (
        outcome === "commit" &&
        Math.abs(current.previewValue - current.startValue) >= current.movementThreshold
      ) {
        current.commit(current.previewValue);
      }
    });
  } finally {
    endShellLayoutBusy();
  }
}

export function cancelActiveResize(): void {
  const current = resize();
  if (current) finishResize(current.id, "cancel");
}

export function beginResize(
  key: ResizeKey,
  startValue: number,
  normalize: (value: number) => number,
  movementThreshold: number,
  commitValue: (value: number) => void,
): ResizeSession {
  cancelActiveResize();
  const id = ++nextResizeId;
  const initial = normalize(Number.isFinite(startValue) ? startValue : 0);
  setResize({
    id,
    key,
    startValue: initial,
    previewValue: initial,
    movementThreshold,
    commit: commitValue,
  });
  beginShellLayoutBusy();

  return {
    preview(value) {
      if (!Number.isFinite(value)) return;
      const next = normalize(value);
      setResize((current) => {
        if (!current || current.id !== id || current.previewValue === next) return current;
        return {
          ...current,
          previewValue: next,
        };
      });
    },
    commit() {
      finishResize(id, "commit");
    },
    cancel() {
      finishResize(id, "cancel");
    },
  };
}

