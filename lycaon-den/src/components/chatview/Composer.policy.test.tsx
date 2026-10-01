import "../../test/composer-view-fixture.tsx";
import { providerModel } from "../../test/provider-fixtures.ts";
import { describe, expect, it } from "vitest";
import { isComposerDisabled } from "../../chat/composer/composer-rules.ts";
import { coordinatorModelVisionSupport } from "../../chat/composer/composer-vision.ts";
import type { ModelPolicy, ProviderMeta, ProviderModelCapabilities } from "../../api/types.ts";



describe("composer-rules", () => {
  it("isComposerDisabled by connection and session, not by folder", () => {
    expect(isComposerDisabled("disconnected", "s1")).toBe(true);
    expect(isComposerDisabled("connected", null)).toBe(true);
    expect(isComposerDisabled("connected", "s1")).toBe(false);
    // Folderless sessions remain chattable.
    expect(isComposerDisabled("connected", "s1", null, null)).toBe(false);
    expect(isComposerDisabled("connected", "s1", null, "*")).toBe(true);
  });
});


describe("composer-vision", () => {
  const capabilities = (
    vision: "supported" | "unsupported",
  ): ProviderModelCapabilities => ({
    chat: { state: "supported" },
    streaming: { state: "supported" },
    tools: { state: "unknown" },
    vision: { state: vision },
    reasoning: { state: "unknown" },
    structured_output: { state: "unknown" },
    prompt_caching: { state: "unknown" },
  });
  const policy: ModelPolicy = {
    coordinator: { provider_id: "openai", model: "gpt-4o" },
    lite: { provider_id: "openai", model: "gpt-4o-mini" },
    agent_pool: { selection: "first", models: [] },
  };

  it("reports vision from the coordinator model entry", () => {
    const providers: ProviderMeta[] = [
      {
        id: "openai",
        kind: "openai",
        configured: true,
        ready_to_assign: true,
        credential_present: true,
        requires_api_key: true,
        features: { tool_calls: true, thinking: false, prompt_cache: "none" },
        models: [
          providerModel("gpt-4o", { capabilities: capabilities("supported") }),
          providerModel("gpt-4o-mini", { capabilities: capabilities("unsupported") }),
        ],
      },
    ];
    expect(coordinatorModelVisionSupport(providers, policy)).toBe("supported");
  });

  it("reports unsupported only when the active model said so", () => {
    const providers: ProviderMeta[] = [
      {
        id: "openai",
        kind: "openai",
        configured: true,
        ready_to_assign: true,
        credential_present: true,
        requires_api_key: true,
        features: { tool_calls: true, thinking: false, prompt_cache: "none" },
        models: [
          providerModel("gpt-4o", { capabilities: capabilities("unsupported") }),
        ],
      },
    ];
    expect(coordinatorModelVisionSupport(providers, policy)).toBe("unsupported");
  });

  // Missing model metadata cannot establish unsupported vision.
  it("reports unknown when the coordinator model is not in the catalog", () => {
    const providers: ProviderMeta[] = [
      {
        id: "openai",
        kind: "openai",
        configured: true,
        ready_to_assign: true,
        credential_present: true,
        requires_api_key: true,
        features: { tool_calls: true, thinking: false, prompt_cache: "none" },
        models: [],
      },
    ];
    expect(coordinatorModelVisionSupport(providers, policy)).toBe("unknown");
  });

  it("reports unknown when no coordinator model is assigned", () => {
    expect(coordinatorModelVisionSupport([], null)).toBe("unknown");
  });
});
