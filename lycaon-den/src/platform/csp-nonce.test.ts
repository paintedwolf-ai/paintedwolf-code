// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { documentStyleNonce } from "./csp-nonce.ts";

afterEach(() => document.head.replaceChildren());

describe("documentStyleNonce", () => {
  it("reads the nonce stamped on the page's own style tags", () => {
    const plain = document.createElement("style");
    const stamped = document.createElement("style");
    stamped.nonce = "abc123";
    document.head.append(plain, stamped);
    expect(documentStyleNonce()).toBe("abc123");
  });

  it("is null when no style tag carries one", () => {
    document.head.append(document.createElement("style"));
    expect(documentStyleNonce()).toBeNull();
  });
});
