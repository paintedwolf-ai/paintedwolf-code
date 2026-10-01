import { CLIENT_NOTICES, type ClientNoticeKind } from "./client-notices.generated.ts";

export class ClientNoticeError extends Error {
  readonly kind: ClientNoticeKind;

  constructor(kind: ClientNoticeKind) {
    const copy = CLIENT_NOTICES[kind];
    super(copy.message);
    this.name = "ClientNoticeError";
    this.kind = kind;
  }
}

export function clientNoticeError(kind: ClientNoticeKind): ClientNoticeError {
  return new ClientNoticeError(kind);
}
