import { createStore } from "solid-js/store";
import type { SessionHostError } from "../api/types.ts";
import type { AppNotice, NoticeInput } from "./notice-model.ts";
import {
  MAX_NOTICE_BUCKETS,
  MAX_NOTICES_PER_SCOPE,
  noticeFromHostError,
  noticeFromInput,
  noticeFromUnknown,
} from "./notice-model.ts";
import { shouldReportToNoticeRail } from "./report-policy.ts";
import type { NoticeScope } from "./notice-scope.ts";
import { APP_SCOPE, noticeScopeKey } from "./notice-scope.ts";

/** Scope key → that scope's notices, oldest first. */
export type NoticeIndex = ReadonlyMap<string, readonly AppNotice[]>;

type NoticeStoreState = {
  buckets: Record<string, AppNotice[]>;
  /** Bucket keys in least-recently-written order, for the bucket cap. */
  order: string[];
};

/** Reporter bound to an operation's scope. */
export type NoticeReporter = {
  reportError: (err: unknown) => void;
  publish: (input: NoticeInput) => void;
};

/** Ephemeral scope-keyed notice state. */
export function createNoticeStore() {
  const [state, setState] = createStore<NoticeStoreState>({
    buckets: {},
    order: [],
  });

  // Cache the index by its reactive bucket reference.
  let cachedBuckets: Record<string, AppNotice[]> | null = null;
  let cachedIndex: NoticeIndex = new Map();

  const index = (): NoticeIndex => {
    const buckets = state.buckets;
    if (buckets !== cachedBuckets) {
      cachedBuckets = buckets;
      const next = new Map<string, readonly AppNotice[]>();
      for (const [key, rows] of Object.entries(buckets)) {
        if (rows.length > 0) next.set(key, rows);
      }
      cachedIndex = next;
    }
    return cachedIndex;
  };

  function dropWhere(match: (notice: AppNotice) => boolean): void {
    setState((prev) => {
      const buckets: Record<string, AppNotice[]> = {};
      for (const [key, rows] of Object.entries(prev.buckets)) {
        const kept = rows.filter((n) => !match(n));
        if (kept.length > 0) buckets[key] = kept;
      }
      return { buckets, order: prev.order.filter((k) => buckets[k]) };
    });
  }

  function add(notice: AppNotice): void {
    const key = noticeScopeKey(notice.scope);
    setState((prev) => {
      const existing = prev.buckets[key] ?? [];

      // Repeated codes refresh one row within their scope.
      const code = notice.code?.trim();
      if (code) {
        const at = existing.findIndex((n) => n.code?.trim() === code);
        const prior = at >= 0 ? existing[at] : undefined;
        if (prior) {
          const merged: AppNotice = {
            ...notice,
            id: prior.id,
            repeats: (prior.repeats ?? 1) + 1,
          };
          const rows = [...existing];
          rows.splice(at, 1);
          rows.push(merged);
          return {
            buckets: { ...prev.buckets, [key]: rows },
            order: touchBucket(prev.order, key),
          };
        }
      }

      const rows = [...existing, notice].slice(-MAX_NOTICES_PER_SCOPE);
      const order = touchBucket(prev.order, key);
      const buckets = { ...prev.buckets, [key]: rows };
      return evictOldestBuckets({ buckets, order });
    });
  }

  return {
    /** Reactive scope-keyed lookup. */
    index,

    /** Publish a Den-authored notice into `scope`. */
    publish(input: NoticeInput, scope: NoticeScope): void {
      add(noticeFromInput(input, scope));
    },

    /** Publish a host error that arrived on the session topic. */
    publishHostError(host: SessionHostError, scope: NoticeScope): void {
      add(noticeFromHostError(host, scope));
    },

    /** Dismiss one notice across every rendered surface. */
    dismiss(id: string): void {
      setState((prev) => {
        const buckets: Record<string, AppNotice[]> = {};
        for (const [key, rows] of Object.entries(prev.buckets)) {
          const kept = rows.filter((n) => n.id !== id);
          if (kept.length > 0) buckets[key] = kept;
        }
        return { buckets, order: prev.order.filter((k) => buckets[k]) };
      });
    },

    /** Dismiss every notice in one scope. */
    dismissScope(scope: NoticeScope): void {
      const key = noticeScopeKey(scope);
      setState((prev) => {
        if (!prev.buckets[key]) return prev;
        const buckets = { ...prev.buckets };
        delete buckets[key];
        return { buckets, order: prev.order.filter((k) => k !== key) };
      });
    },

    /** Drop a session's notices. */
    clearSession(sessionId: string): void {
      dropWhere((n) => n.scope.kind === "session" && n.scope.sessionId === sessionId);
    },

    /** Drop a project's notices, and those of its sessions. */
    clearProject(projectId: string): void {
      dropWhere((n) => n.scope.kind !== "app" && n.scope.projectId === projectId);
    },

    /** Clear session host errors after a completed clean turn. */
    clearSessionHostErrors(sessionId: string): void {
      dropWhere(
        (n) =>
          n.source === "host_error" &&
          n.scope.kind === "session" &&
          n.scope.sessionId === sessionId,
      );
    },

    /** A reporter bound to one scope. */
    reporterFor(scope: NoticeScope): NoticeReporter {
      return {
        reportError: (err: unknown) => {
          if (!shouldReportToNoticeRail(err)) return;
          add(noticeFromUnknown(err, scope));
        },
        publish: (input: NoticeInput) => add(noticeFromInput(input, scope)),
      };
    },
  };
}

export type NoticeStore = ReturnType<typeof createNoticeStore>;

let publishStoreRef: NoticeStore | null = null;

/** Registers the notice publisher for non-component dispatch. */
export function registerNoticePublisher(store: NoticeStore | null): void {
  publishStoreRef = store;
}

/** Clear host errors from a session completion event. */
export function clearSessionHostErrorNotices(sessionId: string): void {
  publishStoreRef?.clearSessionHostErrors(sessionId);
}

export function publishSessionHostError(
  host: SessionHostError,
  projectId: string,
  sessionId: string,
): void {
  publishStoreRef?.publishHostError(host, {
    kind: "session",
    projectId,
    sessionId,
  });
}

/** Publishes a Den-authored notice from outside a component; app scope by default. */
export function publishNotice(input: NoticeInput, scope: NoticeScope = APP_SCOPE): void {
  publishStoreRef?.publish(input, scope);
}

/** Report a caught request failure against one session, with the host's own copy for a typed error. */
export function reportSessionNoticeError(
  err: unknown,
  projectId: string,
  sessionId: string,
): void {
  publishStoreRef
    ?.reporterFor({ kind: "session", projectId, sessionId })
    .reportError(err);
}

/** Report a caught request failure against one project, with the host's own copy for a typed error. */
export function reportProjectNoticeError(err: unknown, projectId: string): void {
  publishStoreRef?.reporterFor({ kind: "project", projectId }).reportError(err);
}

function touchBucket(order: readonly string[], key: string): string[] {
  return [...order.filter((k) => k !== key), key];
}

/** Enforce the global bucket cap. */
function evictOldestBuckets(next: NoticeStoreState): NoticeStoreState {
  if (next.order.length <= MAX_NOTICE_BUCKETS) return next;
  const drop = next.order.slice(0, next.order.length - MAX_NOTICE_BUCKETS);
  const buckets = { ...next.buckets };
  for (const key of drop) delete buckets[key];
  return { buckets, order: next.order.slice(drop.length) };
}
