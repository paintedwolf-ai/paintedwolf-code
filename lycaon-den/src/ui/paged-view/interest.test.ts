import { expect, it } from "vitest";
import { withInterest } from "./interest.ts";

it("preserves Error identities from shared work", async () => {
  const error = new Error("Read failed");
  await expect(withInterest(Promise.reject(error))).rejects.toBe(error);
});

it("retains non-Error rejection values as the cause", async () => {
  const cause = { code: "read_failed" };
  // eslint-disable-next-line @typescript-eslint/prefer-promise-reject-errors -- Exercises a non-Error rejection.
  await expect(withInterest(Promise.reject(cause))).rejects.toMatchObject({
    message: "The presentation request failed.",
    cause,
  });
});

it("canceling one presenter leaves the other presenter's result intact", async () => {
  const controller = new AbortController();
  let finish!: (value: string) => void;
  const work = new Promise<string>((resolve) => { finish = resolve; });
  const canceled = withInterest(work, controller.signal);
  const retained = withInterest(work);
  const rejection = expect(canceled).rejects.toMatchObject({ name: "AbortError" });
  controller.abort();
  await rejection;
  finish("ready");
  await expect(retained).resolves.toBe("ready");
});
