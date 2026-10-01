import type { SessionHostError } from "../api/types.ts";
import { LycaonApiError } from "../api/http.ts";
import { isBackendUnreachableError } from "../platform/connection/request-connectivity.ts";
import { CLIENT_NOTICES, type ClientNoticeKind } from "./client-notices.generated.ts";
import { ClientNoticeError } from "./client-notices.ts";
import type { NoticeScope } from "./notice-scope.ts";
import { scopeOfKind } from "./notice-routing.ts";
import { clientNoticeScope } from "./notice-routing.ts";

type NoticeSeverity = "error" | "warning" | "info";

export type AppNotice = {
  id: string;
  severity: NoticeSeverity;
  title: string;
  message: string;
  code?: string;
  suggestedAction?: string;
  actions?: readonly string[];
  createdAt: number;
  scope: NoticeScope;
  resolution?: string;
  repeats?: number;
  /** Host errors retire after a clean turn. */
  source?: "host_error";
};

export type NoticeInput = {
  severity?: NoticeSeverity;
  title?: string;
  message: string;
  code?: string;
  suggestedAction?: string;
  actions?: readonly string[];
};

const UNKNOWN_NOTICE = CLIENT_NOTICES.unexpected_client_error;

type NoticeWire = {
  code?: string;
  title?: string;
  message?: string;
  suggested_action?: string;
  actions?: readonly string[];
  scope?: string;
  resolution?: string;
};

let noticeSeq = 0;

function nextNoticeId(): string {
  noticeSeq += 1;
  return `notice-${noticeSeq}-${Date.now()}`;
}

export function noticeFromWire(w: NoticeWire, callSite: NoticeScope): AppNotice {
  const code = w.code?.trim();
  const title = w.title?.trim();
  const message = w.message?.trim();
  const suggestedAction =
    w.suggested_action?.trim() || UNKNOWN_NOTICE.suggestedAction;
  const scope = resolveWireScope(w, callSite);
  const resolution = w.resolution?.trim() || undefined;
  const actions = w.actions && w.actions.length > 0
    ? w.actions.map((a) => a.trim()).filter(Boolean)
    : undefined;

  if (title && message) {
    return {
      id: nextNoticeId(),
      severity: "error",
      title,
      message,
      code,
      suggestedAction,
      actions,
      createdAt: Date.now(),
      scope,
      resolution,
    };
  }

  return {
    id: nextNoticeId(),
    severity: "error",
    title: UNKNOWN_NOTICE.title,
    message: message || UNKNOWN_NOTICE.message,
    code,
    suggestedAction,
    actions,
    createdAt: Date.now(),
    scope,
    resolution,
  };
}

function resolveWireScope(w: NoticeWire, callSite: NoticeScope): NoticeScope {
  const declared = w.scope?.trim();
  if (declared === "app" || declared === "project" || declared === "session") {
    return scopeOfKind(declared, callSite);
  }
  return callSite;
}

export function noticeFromHostError(
  host: SessionHostError,
  callSite: NoticeScope,
): AppNotice {
  return {
    ...noticeFromWire(
      {
        code: host.code,
        title: host.title,
        message: host.message,
        suggested_action: host.suggested_action,
        actions: host.actions,
        scope: host.scope,
        resolution: host.resolution,
      },
      callSite,
    ),
    source: "host_error",
  };
}

export function noticeFromUnknown(err: unknown, callSite: NoticeScope): AppNotice {
  if (err instanceof ClientNoticeError) {
    const copy = CLIENT_NOTICES[err.kind];
    return noticeFromWire(
      {
        code: err.kind,
        title: copy.title,
        message: copy.message,
        actions: copy.action ? [copy.action] : undefined,
      },
      clientNoticeScope(err.kind, callSite),
    );
  }
  if (err instanceof LycaonApiError) {
    return noticeFromWire(
      {
        code: err.code,
        title: err.title,
        message: err.message,
        suggested_action: err.suggestedAction,
        actions: err.actions,
        scope: err.scope,
        resolution: err.resolution,
      },
      callSite,
    );
  }
  if (isBackendUnreachableError(err)) {
    const copy = CLIENT_NOTICES.fetch_failure;
    const kind: ClientNoticeKind = "fetch_failure";
    return noticeFromWire(
      { code: kind, title: copy.title, message: copy.message },
      clientNoticeScope(kind, callSite),
    );
  }
  // Untyped exceptions use stable catalog copy.
  return noticeFromWire(
    { message: UNKNOWN_NOTICE.message, title: UNKNOWN_NOTICE.title },
    callSite,
  );
}

export function noticeFromCaught(
  err: unknown,
  callSite: NoticeScope,
  fallback?: NoticeInput,
): AppNotice {
  if (err instanceof ClientNoticeError || err instanceof LycaonApiError) {
    return noticeFromUnknown(err, callSite);
  }
  if (fallback) {
    return noticeFromInput(fallback, callSite);
  }
  return noticeFromUnknown(err, callSite);
}

export function noticeFromCopy(
  copy: {
    title?: string;
    message?: string;
    suggested_action?: string;
    actions?: readonly string[];
  } | undefined,
  code: string | undefined,
  callSite: NoticeScope,
): AppNotice | undefined {
  if (!copy) return undefined;
  const title = copy.title?.trim();
  const message = copy.message?.trim();
  if (!title || !message) return undefined;
  return noticeFromWire(
    {
      code,
      title,
      message,
      suggested_action: copy.suggested_action,
      actions: copy.actions,
    },
    callSite,
  );
}

export function noticeFromInput(input: NoticeInput, callSite: NoticeScope): AppNotice {
  const notice = noticeFromWire(
    {
      code: input.code,
      title: input.title,
      message: input.message,
      suggested_action: input.suggestedAction,
      actions: input.actions,
    },
    callSite,
  );
  return {
    ...notice,
    severity: input.severity ?? "error",
  };
}

export const MAX_NOTICES_PER_SCOPE = 4;

export const MAX_NOTICE_BUCKETS = 32;
