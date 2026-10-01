import { APP_SCOPE } from "./notice-scope.ts";
import { describe, expect, it } from "vitest";
import { LycaonApiError } from "../api/http.ts";
import { clientNoticeError } from "./client-notices.ts";
import { BackendTransportError } from "../platform/connection/request-connectivity.ts";
import {
  noticeFromCaught,
  noticeFromCopy,
  noticeFromHostError,
  noticeFromInput,
  noticeFromUnknown,
  noticeFromWire,
} from "./notice-model.ts";

describe("noticeFromWire", () => {
  it("maps server-rendered fields directly", () => {
    const notice = noticeFromWire({
      code: "provider_not_configured",
      title: "No model configured",
      message: "Provider fireworks is not set up yet.",
      suggested_action: "Open Settings → AI providers",
      actions: ["open_ai_providers"],
    }, APP_SCOPE);
    expect(notice.title).toBe("No model configured");
    expect(notice.message).toContain("fireworks");
    expect(notice.actions).toEqual(["open_ai_providers"]);
  });

  it("uses unknown fallback when title or message missing", () => {
    const notice = noticeFromWire({ code: "mystery" }, APP_SCOPE);
    expect(notice.message).toContain("did not complete");
    // defaults.yaml must not read as an authorization denial.
    expect(notice.message).not.toContain("blocked");
    expect(notice.message).not.toBe("mystery");
  });

  it("maps actions array directly", () => {
    const notice = noticeFromWire({
      code: "provider_server_error",
      title: "The model provider had a server error",
      message: "Server failed",
      actions: ["prompt_keep_going", "prompt_rewind_and_retry"],
    }, APP_SCOPE);
    expect(notice.actions).toEqual(["prompt_keep_going", "prompt_rewind_and_retry"]);
  });
});

describe("noticeFromHostError", () => {
  it("displays wire copy only", () => {
    const notice = noticeFromHostError({
      code: "provider_empty_completion",
      title: "Model returned no response",
      message: "llama-3.1-8b finished without any text or tool calls.",
    }, APP_SCOPE);
    expect(notice.message).toContain("llama-3.1-8b");
    expect(notice.message).not.toContain("debug");
  });

  it("maps host error actions", () => {
    const notice = noticeFromHostError({
      code: "provider_server_error",
      title: "The model provider had a server error",
      message: "Server failed",
      actions: ["prompt_keep_going", "prompt_rewind_and_retry"],
    }, APP_SCOPE);
    expect(notice.actions).toEqual(["prompt_keep_going", "prompt_rewind_and_retry"]);
  });
});

describe("noticeFromUnknown", () => {
  it("reads LycaonApiError wire fields", () => {
    const notice = noticeFromUnknown(
      new LycaonApiError("Provider fireworks is not set up yet.", 503, "provider_not_configured", {
        title: "No model configured",
        suggestedAction: "Set a key",
      }),
      APP_SCOPE,
    );
    expect(notice.title).toBe("No model configured");
    expect(notice.suggestedAction).toBe("Set a key");
  });

  it("maps client notice errors", () => {
    const notice = noticeFromUnknown(clientNoticeError("offline"), APP_SCOPE);
    expect(notice.title).toBe("Cannot connect");
  });

  it("maps confirmed backend unreachability to client copy", () => {
    const notice = noticeFromUnknown(
      new BackendTransportError(
        new TypeError("localized"),
        "unreachable",
      ),
      APP_SCOPE,
    );
    expect(notice.title).toBe("Could not connect");
    expect(notice.message).toContain("lost its connection");
  });

  it("does not infer connectivity from arbitrary browser error copy", () => {
    const notice = noticeFromUnknown(new TypeError("Load failed"), APP_SCOPE);
    expect(notice.title).not.toBe("Could not connect");
  });

  it("does not expose arbitrary exception text as notice copy", () => {
    const notice = noticeFromUnknown(
      new Error("sqlite at /Users/someone/private/store.db failed"),
      APP_SCOPE,
    );
    expect(notice.message).not.toContain("/Users/");
    expect(notice.message).toContain("did not complete");
  });
});

describe("noticeFromCaught", () => {
  it("keeps host copy on LycaonApiError", () => {
    const notice = noticeFromCaught(
      new LycaonApiError("A remote MCP provider can only be reached over HTTPS.", 400, "remote_requires_https", {
        title: "Remote MCP must use HTTPS",
        suggestedAction: "Use an https:// URL.",
      }),
      APP_SCOPE,
      { title: "Fallback", message: "unused" },
    );
    expect(notice.title).toBe("Remote MCP must use HTTPS");
    expect(notice.code).toBe("remote_requires_https");
  });

  it("uses fallback when the error is not catalog-backed", () => {
    const notice = noticeFromCaught(new Error("disk full"), APP_SCOPE, {
      title: "Could not save",
      message: "The overlay file could not be written.",
    });
    expect(notice.title).toBe("Could not save");
    expect(notice.message).toContain("overlay");
  });
});

describe("noticeFromCopy", () => {
  it("returns undefined when title or message is missing", () => {
    expect(
      noticeFromCopy({ title: "Only title" }, "remote_requires_https", APP_SCOPE),
    ).toBeUndefined();
  });

  it("maps a row notice onto the shared model", () => {
    const notice = noticeFromCopy(
      {
        title: "Remote MCP must use HTTPS",
        message: "A remote MCP provider can only be reached over HTTPS.",
        suggested_action: "Use an https:// URL.",
      },
      "remote_requires_https",
      APP_SCOPE,
    );
    expect(notice?.code).toBe("remote_requires_https");
    expect(notice?.suggestedAction).toContain("https://");
  });
});

describe("noticeFromInput", () => {
  it("accepts a caller-supplied fallback", () => {
    const notice = noticeFromInput(
      { title: "Could not save", message: "Try again." },
      APP_SCOPE,
    );
    expect(notice.title).toBe("Could not save");
  });
});
