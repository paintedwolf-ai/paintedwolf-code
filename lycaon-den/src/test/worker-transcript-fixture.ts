import type { Message } from "../api/types.ts";
import {
  emptyTranscriptWindow,
  installTailWindow,
  materializeTranscriptWindow,
} from "../chat/transcript/layout/transcript-window.ts";
import type { WorkerTranscript } from "../chat/worker/worker-transcript.ts";

/** A hydrated worker transcript whose live tail holds `rows`. */
export function workerTranscriptFixture(
  rows: readonly Message[],
  opts: { hasMoreBefore?: boolean; hydrated?: boolean } = {},
): WorkerTranscript {
  const window = installTailWindow(
    emptyTranscriptWindow(),
    { messages: [...rows], hasMoreBefore: opts.hasMoreBefore ?? false, hasMoreAfter: false },
    { resetPages: true },
  );
  return { window, rows: materializeTranscriptWindow(window), hydrated: opts.hydrated ?? true };
}
