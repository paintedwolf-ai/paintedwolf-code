import { createRoot } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { createCheckpointSubmission } from "./checkpoint-submission.ts";

const scope = sessionScope("project", "session");
const capacity = () => new LycaonApiError("The action is still waiting for your decision.", 422, "approval_capacity_exceeded", {
  title: "Approval could not be applied",
  suggestedAction: "Reject the action or stop the session.",
  retryable: false,
});

function submissions() {
  return createRoot((dispose) => ({ submission: createCheckpointSubmission(), dispose }));
}

describe("checkpoint submission", () => {
  it("keeps the host explanation with the failed decision until another answer succeeds", async () => {
    const { submission, dispose } = submissions();
    submission.retain(["card"]);
    expect(await submission.run("card", scope, async () => { throw capacity(); })).toBe(false);
    expect(submission.pending("card")).toBe(false);
    expect(submission.notice("card")).toMatchObject({ code: "approval_capacity_exceeded", title: "Approval could not be applied", suggestedAction: "Reject the action or stop the session.", scope });
    expect(await submission.run("card", scope, async () => {})).toBe(true);
    expect(submission.notice("card")).toBeUndefined();
    expect(submission.pending("card")).toBe(true);
    const duplicate = vi.fn(async () => {});
    expect(await submission.run("card", scope, duplicate)).toBe(false);
    expect(duplicate).not.toHaveBeenCalled();
    submission.retain([]);
    expect(submission.pending("card")).toBe(false);
    dispose();
  });

  it("deduplicates a decision while keeping other checkpoints independent", async () => {
    const { submission, dispose } = submissions();
    let complete!: () => void;
    const operation = vi.fn(() => new Promise<void>((resolve) => { complete = resolve; }));
    const first = submission.run("one", scope, operation);
    expect(submission.pending("one")).toBe(true);
    expect(await submission.run("one", scope, operation)).toBe(false);
    expect(await submission.run("two", scope, async () => { throw capacity(); })).toBe(false);
    expect(submission.pending("one")).toBe(true);
    expect(submission.notice("two")?.code).toBe("approval_capacity_exceeded");
    complete();
    expect(await first).toBe(true);
    expect(operation).toHaveBeenCalledTimes(1);
    expect(submission.notice("two")?.code).toBe("approval_capacity_exceeded");
    dispose();
  });

  it("retires removed cards even when a late decision fails", async () => {
    const { submission, dispose } = submissions();
    let fail!: (error: unknown) => void;
    const pending = submission.run("old", scope, () => new Promise<void>((_, reject) => { fail = reject; }));
    submission.retain(["new"]);
    fail(capacity());
    expect(await pending).toBe(false);
    expect(submission.notice("old")).toBeUndefined();
    expect(submission.pending("old")).toBe(false);
    dispose();
  });

  it("leaves connectivity feedback with its existing owner", async () => {
    const { submission, dispose } = submissions();
    expect(await submission.run("card", scope, async () => { throw new TypeError("private transport detail"); })).toBe(false);
    expect(submission.notice("card")).toBeUndefined();
    dispose();
  });

  it("ignores completions after disposal", async () => {
    const { submission, dispose } = submissions();
    let fail!: (error: unknown) => void;
    const pending = submission.run("card", scope, () => new Promise<void>((_, reject) => { fail = reject; }));
    dispose();
    fail(capacity());
    expect(await pending).toBe(false);
    expect(submission.notice("card")).toBeUndefined();
    expect(await submission.run("card", scope, async () => {})).toBe(false);
  });
});
