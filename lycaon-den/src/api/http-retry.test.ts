import { expect, it } from "vitest";
import { LycaonApiError, requireSuccessfulResponse } from "./http.ts";

it("preserves Retry-After with the structured failure", async () => {
  const response = new Response(JSON.stringify({ code: "rate_limited", message: "Busy", retryable: true }), {
    status: 429, headers: { "Retry-After": "3" },
  });
  await expect(requireSuccessfulResponse(response)).rejects.toMatchObject({
    status: 429, code: "rate_limited", retryable: true, retryAfterMs: 3000,
  });
});

it("does not manufacture a retry deadline from an invalid header", async () => {
  const result = requireSuccessfulResponse(new Response("", { status: 503, headers: { "Retry-After": "later" } }));
  await expect(result).rejects.toBeInstanceOf(LycaonApiError);
  await expect(result).rejects.toMatchObject({ retryAfterMs: undefined });
});
