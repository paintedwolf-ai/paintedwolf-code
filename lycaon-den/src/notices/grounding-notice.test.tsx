import { afterEach, describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { NoticeRail } from "../components/NoticeRail.tsx";
import {
  EVIDENCE_BODY_UNAVAILABLE_CODE,
  publishGroundingEventNotice,
} from "./grounding-notice.ts";
import { noticeScopeKey } from "./notice-scope.ts";
import {
  clearSessionHostErrorNotices,
  createNoticeStore,
  registerNoticePublisher,
} from "./notice-store.ts";

describe("publishGroundingEventNotice", () => {
  afterEach(() => {
    registerNoticePublisher(null);
  });

  it("publishes a human-facing session warning for EVIDENCE_BODY_UNAVAILABLE and renders it in NoticeRail", () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);

    publishGroundingEventNotice(
      {
        session_id: "sess-1",
        code: EVIDENCE_BODY_UNAVAILABLE_CODE,
      },
      "proj-1",
    );

    const scopeKey = noticeScopeKey({
      kind: "session",
      projectId: "proj-1",
      sessionId: "sess-1",
    });
    const rows = store.index().get(scopeKey) ?? [];
    expect(rows).toHaveLength(1);
    expect(rows[0]?.severity).toBe("warning");
    expect(rows[0]?.code).toBe(EVIDENCE_BODY_UNAVAILABLE_CODE);
    expect(rows[0]?.title).toBe("Saved file copy was damaged");
    expect(rows[0]?.message).toContain(
      "A saved copy of something the agent read was damaged, so the agent is being asked to read it again before citing it.",
    );
    expect(rows[0]?.suggestedAction).toBe(
      "No action is needed now. If this keeps happening, ask the agent to read the file again.",
    );

    const { container } = render(() => (
      <NoticeRail notices={rows} onDismiss={(id) => store.dismiss(id)} />
    ));
    expect(container.textContent).toContain("Saved file copy was damaged");
    expect(container.textContent).toContain(
      "A saved copy of something the agent read was damaged, so the agent is being asked to read it again before citing it.",
    );
    expect(container.textContent).toContain(
      "No action is needed now. If this keeps happening, ask the agent to read the file again.",
    );
  });

  it("ignores unrelated grounding event codes", () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);

    publishGroundingEventNotice(
      { session_id: "sess-1", code: "GROUNDING_OTHER" },
      "proj-1",
    );

    expect(store.index().size).toBe(0);
  });

  it("retires the EVIDENCE_BODY_UNAVAILABLE notice after a clean completed turn", () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);

    publishGroundingEventNotice(
      { session_id: "sess-1", code: EVIDENCE_BODY_UNAVAILABLE_CODE },
      "proj-1",
    );
    const scopeKey = noticeScopeKey({
      kind: "session",
      projectId: "proj-1",
      sessionId: "sess-1",
    });
    expect(store.index().get(scopeKey)).toHaveLength(1);

    clearSessionHostErrorNotices("sess-1");
    expect(store.index().get(scopeKey)).toBeUndefined();
  });
});
