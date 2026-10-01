import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createNoticeStore,
  publishNotice,
  registerNoticePublisher,
} from "./notice-store.ts";
import {
  APP_SCOPE,
  projectScope,
  sessionScope,
} from "./notice-scope.ts";
import {
  selectAppNotices,
  selectProjectNoticeGroups,
  selectSessionNotices,
} from "./notice-select.ts";
import { MAX_NOTICE_BUCKETS, MAX_NOTICES_PER_SCOPE } from "./notice-model.ts";

const A = sessionScope("proj-1", "sess-a");
const B = sessionScope("proj-1", "sess-b");

afterEach(() => {
  vi.restoreAllMocks();
});

function publish(store: ReturnType<typeof createNoticeStore>, scope = A, code?: string) {
  store.publish({ message: "boom", title: "Boom", code }, scope);
}

function projectNotices(
  store: ReturnType<typeof createNoticeStore>,
  projectId: string,
) {
  return (
    selectProjectNoticeGroups(store.index()).find(
      (group) => group.projectId === projectId,
    )?.notices ?? []
  );
}

describe("notice store scoping", () => {
  it("keeps one chat's notice out of another chat's dock", () => {
    const store = createNoticeStore();
    publish(store, A);

    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(1);
    expect(selectSessionNotices(store.index(), "sess-b")).toHaveLength(0);
  });

  it("survives switching away and back", () => {
    const store = createNoticeStore();
    publish(store, A);

    expect(selectSessionNotices(store.index(), "sess-b")).toHaveLength(0);
    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(1);
  });

  it("records a background session's notice", () => {
    const store = createNoticeStore();
    store.publishHostError(
      { code: "provider_empty_completion", title: "No response", message: "empty" },
      B,
    );
    expect(selectSessionNotices(store.index(), "sess-b")).toHaveLength(1);
  });

  it("selects every project's notice from one entry", () => {
    const store = createNoticeStore();
    publish(store, projectScope("proj-1"), "PROJECT_NOTICE");

    const forProject = projectNotices(store, "proj-1");
    expect(forProject).toHaveLength(1);
    const id = forProject[0]!.id;
    store.dismiss(id);
    expect(projectNotices(store, "proj-1")).toHaveLength(0);
  });

  it("returns notices for background projects and puts the active project first", () => {
    const store = createNoticeStore();
    publish(store, projectScope("proj-1"), "ONE");
    publish(store, projectScope("proj-2"), "TWO");

    expect(
      selectProjectNoticeGroups(store.index(), "proj-1").map(
        (group) => group.projectId,
      ),
    ).toEqual(["proj-1", "proj-2"]);
    expect(projectNotices(store, "proj-2")).toHaveLength(1);
  });

  it("orders background projects by their newest notice", () => {
    const store = createNoticeStore();
    const now = vi.spyOn(Date, "now");
    now.mockReturnValue(100);
    publish(store, projectScope("proj-1"), "OLDER");
    now.mockReturnValue(200);
    publish(store, projectScope("proj-2"), "NEWER");

    expect(
      selectProjectNoticeGroups(store.index()).map((group) => group.projectId),
    ).toEqual(["proj-2", "proj-1"]);
  });

  it("keeps scopes partitioned", () => {
    const store = createNoticeStore();
    publish(store, APP_SCOPE);
    publish(store, projectScope("proj-1"));
    publish(store, A);

    expect(selectAppNotices(store.index())).toHaveLength(1);
    expect(projectNotices(store, "proj-1")).toHaveLength(1);
    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(1);
  });
});

describe("notice store caps", () => {
  it("caps per scope rather than globally", () => {
    const store = createNoticeStore();
    for (let i = 0; i < MAX_NOTICES_PER_SCOPE + 3; i += 1) {
      publish(store, A, `code-${i}`);
    }
    publish(store, B, "still-here");

    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(
      MAX_NOTICES_PER_SCOPE,
    );
    expect(selectSessionNotices(store.index(), "sess-b")).toHaveLength(1);
  });

  it("supersedes a repeat of the same code in the same scope", () => {
    const store = createNoticeStore();
    publish(store, A, "flapping");
    publish(store, A, "flapping");
    publish(store, A, "flapping");

    const rows = selectSessionNotices(store.index(), "sess-a");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.repeats).toBe(3);
  });

  it("bounds the number of buckets", () => {
    const store = createNoticeStore();
    for (let i = 0; i < MAX_NOTICE_BUCKETS + 5; i += 1) {
      publish(store, sessionScope("proj-1", `sess-${i}`));
    }
    expect(store.index().size).toBeLessThanOrEqual(MAX_NOTICE_BUCKETS);
  });
});

describe("notice store lifecycle", () => {
  it("clears a deleted session", () => {
    const store = createNoticeStore();
    publish(store, A);
    store.clearSession("sess-a");
    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(0);
  });

  it("clears a deleted project and its chats", () => {
    const store = createNoticeStore();
    publish(store, A);
    publish(store, projectScope("proj-1"));
    publish(store, APP_SCOPE);

    store.clearProject("proj-1");

    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(0);
    expect(projectNotices(store, "proj-1")).toHaveLength(0);
    expect(selectAppNotices(store.index())).toHaveLength(1);
  });

  it("retires only host errors when a session completes a clean turn", () => {
    const store = createNoticeStore();
    store.publishHostError({ code: "prompt_failed", title: "t", message: "m" }, A);
    publish(store, A, "rename_failed");

    store.clearSessionHostErrors("sess-a");

    const rows = selectSessionNotices(store.index(), "sess-a");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.code).toBe("rename_failed");
  });

  it("dismisses a whole scope", () => {
    const store = createNoticeStore();
    publish(store, A, "one");
    publish(store, A, "two");
    store.dismissScope(A);
    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(0);
  });
});

describe("host-declared scope", () => {
  it("widens to the scope the host declared", () => {
    const store = createNoticeStore();
    store.publishHostError(
      {
        code: "provider_not_configured",
        title: "No provider",
        message: "configure one",
        scope: "app",
      },
      A,
    );

    expect(selectAppNotices(store.index())).toHaveLength(1);
    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(0);
  });

  it("keeps the call-site scope when the host declares none", () => {
    const store = createNoticeStore();
    store.publishHostError({ code: "prompt_failed", title: "t", message: "m" }, A);
    expect(selectSessionNotices(store.index(), "sess-a")).toHaveLength(1);
  });
});

describe("publishNotice (module-level publish, outside a component)", () => {
  afterEach(() => registerNoticePublisher(null));

  it("delivers to the registered store's app scope by default", () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);

    publishNotice({ title: "Couldn't fill the composer", message: "No project is open." });

    const notices = selectAppNotices(store.index());
    expect(notices).toHaveLength(1);
    expect(notices[0]?.message).toBe("No project is open.");
  });

  it("delivers to an explicit scope", () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);

    publishNotice(
      { title: "Couldn't fill the composer", message: "No chat chosen." },
      projectScope("proj-1"),
    );

    expect(projectNotices(store, "proj-1")).toHaveLength(1);
    expect(selectAppNotices(store.index())).toHaveLength(0);
  });

  it("is a no-op when no store is registered", () => {
    expect(() =>
      publishNotice({ title: "t", message: "m" }),
    ).not.toThrow();
  });
});
