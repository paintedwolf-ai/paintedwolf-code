import { SECRET_SPAN_COPY } from "./secret-span-copy.ts";

/** User-facing error from marking a secret. */
export class MarkSecretFailure extends Error {
  constructor(message: string) {
    super(message);
    this.name = "MarkSecretFailure";
  }
}

/** Returns safe user-facing failure copy. */
export function markFailureMessage(error: unknown): string {
  return error instanceof MarkSecretFailure
    ? error.message
    : SECRET_SPAN_COPY.markFailed;
}
