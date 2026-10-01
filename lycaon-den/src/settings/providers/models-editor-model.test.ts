import { describe, expect, it } from "vitest";
import type { ModelPolicy, ProviderMeta } from "../../api/types.ts";
import {
  addPoolRow,
  assignmentPatch,
  clearProviderFromModelPolicy,
  defaultModelPatch,
  hasDefaultModel,
  providerRemovalPatch,
  summarizerPatch,
  summarizerOverrideRef,
  groupProvidersByKind,
  hasReadyProvider,
  providerCatalogStatusHint,
  providerDiscoveryStatusHint,
  providerIsReady,
  modelAssignmentOptions,
  nextNumberedProviderLabel,
  modelAssignmentOptionGroups,
  modelAssignmentOptionGroupsForPicker,
  providerPickerGroup,
  providerKindIsCustom,
  providerKindTemplateGroups,
  providerPlatformsSupported,
  providerDisplayName,
  providerNeedsApiKey,
  providerShowsApiKeyControls,
  providerDiscoveryFailure,
  providerStatusLabel,
  providerToolCallSupport,
  formatModelAssignmentLabel,
  modelTokenRatesLabel,
  shortModelId,
  policyHasModelAssignments,
  removePoolRow,
  setWorkerPoolEnabled,
  uniqueProviderId,
  usesWorkerPool,
  listedProviders,
  workerModelRef,
} from "./models-editor-model.ts";
import { MODELS_SETTINGS_COPY } from "./models-settings-copy.ts";
import {
  assignableModelCapabilities,
  providerModel,
} from "../../test/provider-fixtures.ts";

const providers: ProviderMeta[] = [
  {
    id: "openai",
    kind: "openai",
    label: "OpenAI",
    base_url: "https://api.openai.com/v1",
    configured: true,
    credential_present: true,
    requires_api_key: true,
    ready_to_assign: true,
    features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
    models: [providerModel("gpt-4o"), providerModel("gpt-4o-mini")],
  },
  {
    id: "fireworks",
    kind: "fireworks",
    label: "Fireworks",
    base_url: "https://api.fireworks.ai/inference/v1",
    configured: false,
    credential_present: false,
    requires_api_key: true,
    ready_to_assign: false,
    features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
    models: [providerModel("accounts/fireworks/models/llama-v3p1-70b-instruct")],
  },
];

const providersWithLocal: ProviderMeta[] = [
  ...providers,
  {
    id: "ollama",
    kind: "ollama",
    label: "Ollama",
    base_url: "http://localhost:11434/v1",
    configured: true,
    credential_present: false,
    requires_api_key: false,
    ready_to_assign: true,
    features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
    models: [providerModel("llama3.1")],
  },
];

const policy: ModelPolicy = {
  coordinator: { provider_id: "openai", model: "gpt-4o" },
  lite: { provider_id: "openai", model: "gpt-4o-mini" },
  agent_pool: {
    selection: "round_robin",
    models: [
      { provider_id: "openai", model: "gpt-4o" },
      { provider_id: "missing", model: "ghost" },
    ],
  },
};

describe("models-editor-model", () => {
  it("modelTokenRatesLabel formats friendly per-1M pricing when present", () => {
    expect(
      modelTokenRatesLabel(providerModel("x", {
        // Rates are stored per 1K tokens in nano USD (1e-9 USD).
        input_per_1k_nano_usd: 10_000_000,
        output_per_1k_nano_usd: 20_000_000,
      })),
    ).toBe("($10/$20 per 1M)");
    expect(
      modelTokenRatesLabel(providerModel("gpt-4o", {
        input_per_1k_nano_usd: 2_500_000,
        output_per_1k_nano_usd: 10_000_000,
      })),
    ).toBe("($2.50/$10 per 1M)");
    expect(modelTokenRatesLabel(providerModel("local"))).toBeUndefined();
  });

  it("shortModelId shortens path-style model ids", () => {
    expect(shortModelId("accounts/fireworks/models/kimi-k2p7-code")).toBe(
      "kimi-k2p7-code",
    );
  });

  it("modelAssignmentOptions carries title and rates separately", () => {
    const priced: ProviderMeta[] = [
      {
        id: "openai",
        kind: "openai",
        label: "OpenAI",
        base_url: "https://api.openai.com/v1",
        configured: true,
        credential_present: true,
        requires_api_key: true,
        ready_to_assign: true,
        features: {
          tool_calls: true,
          thinking: true,
          prompt_cache: "automatic_prefix",
        },
        models: [
          providerModel("gpt-4o", { input_per_1k_nano_usd: 2_500_000, output_per_1k_nano_usd: 10_000_000 }),
        ],
      },
    ];
    expect(modelAssignmentOptions(priced)).toEqual([
      {
        provider_id: "openai",
        model: "gpt-4o",
        title: "OpenAI — gpt-4o",
        rates: "($2.50/$10 per 1M)",
        disabled: false,
        reason: undefined,
        unverified: false,
      },
    ]);
  });

  it("providerNeedsApiKey when key-based provider has no stored credential", () => {
    expect(
      providerNeedsApiKey({
        id: "fireworks",
        kind: "fireworks",
        configured: true,
        ready_to_assign: true,
        credential_present: false,
        requires_api_key: true,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [providerModel("m")],
      }),
    ).toBe(true);
    expect(
      providerNeedsApiKey({
        id: "fireworks",
        kind: "fireworks",
        configured: true,
        ready_to_assign: true,
        credential_present: true,
        requires_api_key: true,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [providerModel("m")],
      }),
    ).toBe(false);
    expect(
      providerNeedsApiKey({
        id: "ollama",
        kind: "ollama",
        configured: true,
        ready_to_assign: true,
        credential_present: false,
        requires_api_key: false,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [providerModel("m")],
      }),
    ).toBe(false);
  });

  it("providerShowsApiKeyControls skips keyless local kinds", () => {
    expect(
      providerShowsApiKeyControls({
        id: "ollama",
        kind: "ollama",
        configured: true,
        ready_to_assign: true,
        credential_present: false,
        requires_api_key: false,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [providerModel("m")],
      }),
    ).toBe(false);
    expect(
      providerShowsApiKeyControls({
        id: "lmstudio",
        kind: "lmstudio",
        configured: true,
        ready_to_assign: true,
        credential_present: false,
        requires_api_key: false,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [],
      }),
    ).toBe(false);
    expect(
      providerShowsApiKeyControls({
        id: "fireworks",
        kind: "fireworks",
        configured: false,
        ready_to_assign: false,
        credential_present: false,
        requires_api_key: true,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [],
      }),
    ).toBe(true);
    expect(
      providerShowsApiKeyControls({
        id: "my-compat",
        kind: "openai-compatible",
        configured: true,
        ready_to_assign: true,
        credential_present: false,
        requires_api_key: false,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [],
      }),
    ).toBe(true);
  });

  it("providerIsReady gates on host ready_to_assign only", () => {
    expect(providerIsReady(providers[0]!)).toBe(true);
    expect(
      providerIsReady({
        id: "ollama",
        kind: "ollama",
        configured: true,
        credential_present: false,
        requires_api_key: false,
        ready_to_assign: false,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        // Readiness comes from the host projection.
        models: [providerModel("llama3.1")],
      }),
    ).toBe(false);
    expect(
      providerIsReady({
        id: "openai",
        kind: "openai",
        configured: true,
        credential_present: true,
        requires_api_key: true,
        ready_to_assign: true,
        catalog_authoritative: true,
        catalog_status: "ok",
        discovery_status: "empty",
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [providerModel("gpt-4.1")],
      }),
    ).toBe(true);
  });

  it("hasReadyProvider requires host ready_to_assign", () => {
    expect(hasReadyProvider(providers)).toBe(true);
    expect(
      hasReadyProvider([
        {
          id: "ollama",
          kind: "ollama",
          configured: true,
          credential_present: false,
          requires_api_key: false,
          ready_to_assign: false,
          features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
          models: [],
        },
        {
          id: "openai",
          kind: "openai",
          configured: false,
          credential_present: false,
          requires_api_key: true,
          ready_to_assign: false,
          features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
          models: [providerModel("gpt-4o")],
        },
      ]),
    ).toBe(false);
  });

  it("modelAssignmentOptions lists only host-provided model ids", () => {
    const hostEligible: ProviderMeta[] = [
      {
        id: "openai",
        kind: "openai",
        configured: true,
        credential_present: true,
        requires_api_key: true,
        ready_to_assign: true,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [providerModel("gpt-4.1"), providerModel("o3")],
      },
    ];
    expect(modelAssignmentOptions(hostEligible).map((o) => o.model)).toEqual([
      "gpt-4.1",
      "o3",
    ]);
  });

  it("feed status hints stay subdued and non-lecture", () => {
    expect(
      providerCatalogStatusHint({
        ...providers[0]!,
        catalog_status: "unavailable",
      }),
    ).toBe("Catalog unavailable");
    expect(
      providerDiscoveryStatusHint({
        ...providers[0]!,
        discovery_status: "empty",
      }),
    ).toBe(MODELS_SETTINGS_COPY.discoveryEmpty);
    // A failed listing is not a subdued chip — it gets a block to itself.
    expect(
      providerDiscoveryStatusHint({
        ...providers[0]!,
        discovery_status: "error",
      }),
    ).toBeNull();
    expect(providerCatalogStatusHint(providers[0]!)).toBeNull();
    expect(providerDiscoveryStatusHint(providers[0]!)).toBeNull();
  });

  it("addPoolRow and removePoolRow adjust agent pool", () => {
    const added = addPoolRow(policy);
    expect(added.agent_pool.models).toHaveLength(3);
    expect(added.agent_pool.models[2]).toEqual({ provider_id: "", model: "" });
    const removed = removePoolRow(added, 1);
    expect(removed.agent_pool.models).toHaveLength(2);
  });

  it("modelAssignmentOptions lists only ready provider models", () => {
    expect(modelAssignmentOptions(providers).map((o) => o.title)).toEqual([
      "OpenAI — gpt-4o",
      "OpenAI — gpt-4o-mini",
    ]);
  });

  it("providerPickerGroup classifies local, cloud, and custom AI providers", () => {
    expect(providerPickerGroup(providersWithLocal[0]!)).toBe("cloud");
    expect(providerPickerGroup(providersWithLocal[2]!)).toBe("local");
    const compat: ProviderMeta = {
      id: "openai-compatible",
      kind: "openai-compatible",
      label: "OpenAI-compatible",
      base_url: "https://api.together.xyz/v1",
      configured: true,
      ready_to_assign: true,
      credential_present: false,
      requires_api_key: false,
      features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
      models: [],
    };
    expect(providerPickerGroup(compat)).toBe("custom");
    expect(providerKindIsCustom("openai-compatible")).toBe(true);
    // Ambient-credential cloud AI providers are keyless but not local.
    const bedrock: ProviderMeta = {
      id: "bedrock",
      kind: "bedrock",
      label: "Amazon Bedrock",
      base_url: "us-east-1",
      configured: true,
      ready_to_assign: true,
      credential_present: false,
      requires_api_key: false,
      features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
      models: [],
    };
    const vertex: ProviderMeta = {
      id: "vertex",
      kind: "vertex",
      label: "Google Vertex AI",
      base_url: "",
      configured: true,
      ready_to_assign: true,
      credential_present: false,
      requires_api_key: false,
      features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
      models: [],
    };
    // Express mode is a keyed https host — cloud by the ordinary path, not the
    // ambient-provider carve-out above it.
    const vertexExpress: ProviderMeta = {
      id: "vertex-express",
      kind: "vertex-express",
      label: "Google Vertex AI (express mode)",
      base_url: "https://aiplatform.googleapis.com/v1",
      configured: true,
      ready_to_assign: true,
      credential_present: true,
      requires_api_key: true,
      features: { tool_calls: true, thinking: true, prompt_cache: "none" },
      models: [],
    };
    expect(providerPickerGroup(bedrock)).toBe("cloud");
    expect(providerPickerGroup(vertex)).toBe("cloud");
    expect(providerPickerGroup(vertexExpress)).toBe("cloud");
    // Keyless http(s) on a LAN host still counts as local (Ollama elsewhere).
    const lanOllama: ProviderMeta = {
      id: "ollama-lan",
      kind: "ollama",
      label: "Ollama (LAN)",
      base_url: "http://192.168.1.10:11434/v1",
      configured: true,
      ready_to_assign: true,
      credential_present: false,
      requires_api_key: false,
      features: { tool_calls: true, thinking: true, prompt_cache: "local_kv" },
      models: [],
    };
    expect(providerPickerGroup(lanOllama)).toBe("local");
  });

  it("modelAssignmentOptionGroups splits options into local, cloud, and custom", () => {
    const withCustom: ProviderMeta[] = [
      ...providersWithLocal,
      {
        id: "openai-compatible",
        kind: "openai-compatible",
        label: "OpenAI-compatible",
        base_url: "https://api.together.xyz/v1",
        configured: true,
        ready_to_assign: true,
        credential_present: true,
        requires_api_key: true,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [providerModel("meta-llama/Llama-3-70b-chat-hf")],
      },
    ];
    const groups = modelAssignmentOptionGroups(withCustom);
    expect(groups.map((g) => [g.label, g.options.map((o) => o.title)])).toEqual([
      ["Run locally", ["Ollama — llama3.1"]],
      ["Cloud / hosted", ["OpenAI — gpt-4o", "OpenAI — gpt-4o-mini"]],
      [
        "Custom",
        ["OpenAI-compatible — Llama-3-70b-chat-hf"],
      ],
    ]);
  });

  it("providerKindTemplateGroups puts OpenAI-compatible in Custom", () => {
    const kinds = [
      {
        kind: "openai-compatible",
        label: "OpenAI-compatible",
        base_url: "http://127.0.0.1:8080/v1",
        requires_api_key: false,
      },
      {
        kind: "openai",
        label: "OpenAI",
        base_url: "https://api.openai.com/v1",
        requires_api_key: true,
      },
      {
        kind: "fireworks",
        label: "Fireworks",
        base_url: "https://api.fireworks.ai/inference/v1",
        requires_api_key: true,
      },
    ];
    const groups = providerKindTemplateGroups(kinds);
    expect(groups.map((g) => [g.label, g.templates.map((t) => t.kind)])).toEqual([
      ["Cloud / hosted", ["fireworks", "openai"]],
      ["Custom", ["openai-compatible"]],
    ]);
  });

  it("modelAssignmentOptionGroupsForPicker keeps orphan assignments in their section", () => {
    const value = { provider_id: "openai", model: "gpt-4o-mini" };
    const groups = modelAssignmentOptionGroupsForPicker([], value);
    expect(groups).toEqual([
      {
        group: "cloud",
        label: "Cloud / hosted",
        options: [
          {
            provider_id: "openai",
            model: "gpt-4o-mini",
            title: formatModelAssignmentLabel(value),
            disabled: true,
            reason: "This assignment is no longer available for this role.",
          },
        ],
      },
    ]);
  });

  it("role filter hides excluded ids but keeps orphan current assignment", () => {
    const gemini: ProviderMeta = {
      id: "gemini",
      kind: "gemini",
      configured: true,
      credential_present: true,
      requires_api_key: true,
      features: { tool_calls: true, thinking: false, prompt_cache: "none" },
      models: [
        providerModel("gemini-2.0-flash"),
        providerModel("gemini-2.5-flash"),
      ],
      ready_to_assign: true,
    };
    gemini.models[0]!.eligibility.coordinator = { state: "incompatible", selectable: false, code: "role_excluded", reason: "Fixture exclusion" };
    const filtered = modelAssignmentOptions([gemini], {
      slot: "coordinator",
      showAll: false,
    }).map((o) => o.model);
    expect(filtered).toEqual(["gemini-2.5-flash"]);
    const orphanGroups = modelAssignmentOptionGroupsForPicker(
      [gemini],
      { provider_id: "gemini", model: "gemini-2.0-flash" },
      { slot: "coordinator", showAll: false },
    );
    expect(
      orphanGroups.flatMap((g) => g.options.map((o) => o.model)),
    ).toEqual(["gemini-2.0-flash", "gemini-2.5-flash"]);
    const showAll = modelAssignmentOptions([gemini], {
      slot: "coordinator",
      showAll: true,
    }).map((o) => o.model);
    expect(showAll).toEqual(["gemini-2.0-flash", "gemini-2.5-flash"]);
  });

  it("does not infer eligibility from model size or context", () => {
    const provider = { ...providers[0]!, models: [providerModel("tiny-1b", { context_length: 8192 })] };
    expect(modelAssignmentOptions([provider], {slot:"coordinator"})).toEqual([expect.objectContaining({model:"tiny-1b",disabled:false})]);
  });

  it("clearProviderFromModelPolicy unsets slots for the removed provider", () => {
    const cleared = clearProviderFromModelPolicy(policy, "openai");
    expect(cleared.coordinator).toBeUndefined();
    expect(cleared.lite).toBeUndefined();
    // Pool entry for a different provider id is left alone.
    expect(cleared.agent_pool.models).toEqual([
      { provider_id: "missing", model: "ghost" },
    ]);
  });

  it("clearProviderFromModelPolicy keeps unrelated provider assignments", () => {
    const mixed: ModelPolicy = {
      coordinator: { provider_id: "openai", model: "gpt-4o" },
      lite: { provider_id: "fireworks", model: "accounts/fireworks/models/llama-v3p1-70b-instruct" },
      agent_pool: {
        selection: "round_robin",
        models: [
          { provider_id: "openai", model: "gpt-4o" },
          { provider_id: "fireworks", model: "accounts/fireworks/models/llama-v3p1-70b-instruct" },
        ],
      },
    };
    const cleared = clearProviderFromModelPolicy(mixed, "openai");
    expect(cleared.coordinator).toBeUndefined();
    expect(cleared.lite).toEqual({
      provider_id: "fireworks",
      model: "accounts/fireworks/models/llama-v3p1-70b-instruct",
    });
    expect(cleared.agent_pool.models).toEqual([
      {
        provider_id: "fireworks",
        model: "accounts/fireworks/models/llama-v3p1-70b-instruct",
      },
    ]);
  });

  it("listedProviders returns platform-supported host instances", () => {
    expect(listedProviders(providers).map((p) => p.id).sort()).toEqual(
      [...providers.map((p) => p.id)].sort(),
    );
  });

  it("providerDisplayName prefers label, falls back to id", () => {
    expect(providerDisplayName(providers[0]!)).toBe("OpenAI");
    expect(
      providerDisplayName({
        id: "ollama-gpu",
        kind: "ollama",
        configured: true,
        ready_to_assign: true,
        credential_present: false,
        requires_api_key: false,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [],
      }),
    ).toBe("ollama-gpu");
  });

  it("providerKindTemplateGroups splits add-provider kinds into local, cloud, and custom", () => {
    const kinds = [
      {
        kind: "ollama",
        label: "Ollama",
        base_url: "http://localhost:11434/v1",
        requires_api_key: false,
      },
      {
        kind: "omlx",
        label: "oMLX",
        base_url: "http://localhost:8000/v1",
        requires_api_key: false,
        platforms: ["macos" as const],
      },
      {
        kind: "openai-compatible",
        label: "OpenAI-compatible",
        base_url: "http://127.0.0.1:8080/v1",
        requires_api_key: false,
      },
      {
        kind: "fireworks",
        label: "Fireworks",
        base_url: "https://api.fireworks.ai/inference/v1",
        requires_api_key: true,
      },
      {
        kind: "openai",
        label: "OpenAI",
        base_url: "https://api.openai.com/v1",
        requires_api_key: true,
      },
    ];
    const groups = providerKindTemplateGroups(kinds);
    // oMLX is macos-gated; when Den platform is unknown (Vite tests) it stays
    // visible and groups under Run locally with other localhost keyless kinds.
    expect(groups.map((g) => [g.label, g.templates.map((t) => t.kind)])).toEqual([
      ["Run locally", ["ollama", "omlx"]],
      ["Cloud / hosted", ["fireworks", "openai"]],
      ["Custom", ["openai-compatible"]],
    ]);
  });

  it("providerPlatformsSupported gates macos-only kinds", () => {
    expect(providerPlatformsSupported(undefined, "linux")).toBe(true);
    expect(providerPlatformsSupported([], "linux")).toBe(true);
    expect(providerPlatformsSupported(["macos"], "macos")).toBe(true);
    expect(providerPlatformsSupported(["macos"], "linux")).toBe(false);
    expect(providerPlatformsSupported(["macos"], null)).toBe(true);
  });

  it("groupProvidersByKind groups instances under the canonical kind label", () => {
    const withInstance: ProviderMeta[] = [
      ...providers,
      {
        id: "fireworks-work",
        kind: "fireworks",
        label: "Fireworks (work)",
        base_url: "https://api.fireworks.ai/inference/v1",
        configured: true,
        ready_to_assign: true,
        credential_present: true,
        requires_api_key: true,
        features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
        models: [providerModel("accounts/fireworks/models/kimi-k2p6")],
      },
    ];
    const kinds = [
      {
        kind: "fireworks",
        label: "Fireworks",
        base_url: "https://api.fireworks.ai/inference/v1",
        requires_api_key: true,
      },
      {
        kind: "openai",
        label: "OpenAI",
        base_url: "https://api.openai.com/v1",
        requires_api_key: true,
      },
    ];
    const groups = groupProvidersByKind(withInstance, kinds);
    expect(groups.map((g) => [g.kind, g.label])).toEqual([
      ["fireworks", "Fireworks"],
      ["openai", "OpenAI"],
    ]);
    // The canonical id===kind instance sorts ahead of clones within its group.
    const fireworks = groups.find((g) => g.kind === "fireworks");
    expect(fireworks?.providers.map((p) => p.id)).toEqual([
      "fireworks",
      "fireworks-work",
    ]);
  });

  it("uniqueProviderId slugifies and avoids collisions", () => {
    expect(uniqueProviderId("GPU box", new Set())).toBe("gpu-box");
    expect(uniqueProviderId("ollama", new Set(["ollama"]))).toBe("ollama-2");
    expect(
      uniqueProviderId("ollama", new Set(["ollama", "ollama-2"])),
    ).toBe("ollama-3");
  });

  it("nextNumberedProviderLabel counts up from 1, skipping used labels", () => {
    const gemini = (label: string): ProviderMeta => ({
      id: label.toLowerCase().replace(/\s+/g, "-"),
      kind: "google_gemini",
      label,
      base_url: "https://generativelanguage.googleapis.com/v1beta/openai",
      configured: true,
      ready_to_assign: true,
      credential_present: true,
      requires_api_key: true,
      features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
      models: [],
    });
    // Only the unnumbered default present → first auto name is "… 1".
    expect(nextNumberedProviderLabel("Google Gemini", [gemini("Google Gemini")])).toBe(
      "Google Gemini 1",
    );
    // "1" already taken → next is "2".
    expect(
      nextNumberedProviderLabel("Google Gemini", [
        gemini("Google Gemini"),
        gemini("Google Gemini 1"),
      ]),
    ).toBe("Google Gemini 2");
    // No instances yet → still starts at 1.
    expect(nextNumberedProviderLabel("Ollama", [])).toBe("Ollama 1");
  });

  it("modelAssignmentOptions lists ready host providers only", () => {
    expect(modelAssignmentOptions(providers).map((o) => o.title)).toEqual([
      "OpenAI — gpt-4o",
      "OpenAI — gpt-4o-mini",
    ]);
  });

  it("policyHasModelAssignments detects saved refs", () => {
    expect(policyHasModelAssignments(policy)).toBe(true);
    expect(
      policyHasModelAssignments({
        ...policy,
        coordinator: { provider_id: "", model: "" },
        lite: { provider_id: "", model: "" },
        agent_pool: { selection: "round_robin", models: [] },
      }),
    ).toBe(false);
  });

  it("worker pool helpers toggle multi-model mode", () => {
    expect(usesWorkerPool(policy)).toBe(true);
    expect(workerModelRef(policy).model).toBe("gpt-4o");

    const single: ModelPolicy = {
      ...policy,
      agent_pool: {
        selection: "round_robin",
        models: [{ provider_id: "openai", model: "gpt-4o-mini" }],
      },
    };
    expect(usesWorkerPool(single)).toBe(false);
    expect(workerModelRef(single).model).toBe("gpt-4o-mini");

    const enabled = setWorkerPoolEnabled(single, true);
    expect(usesWorkerPool(enabled)).toBe(true);
    expect(enabled.agent_pool.selection).toBe("random");
    expect(enabled.agent_pool.models).toEqual([
      { provider_id: "openai", model: "gpt-4o-mini" },
      { provider_id: "", model: "" },
    ]);

    const disabled = setWorkerPoolEnabled(enabled, false);
    expect(usesWorkerPool(disabled)).toBe(false);
    expect(disabled.agent_pool.models).toHaveLength(1);
  });
});

describe("providerToolCallSupport", () => {
  const base: ProviderMeta = {
    id: "p",
    kind: "p",
    configured: true,
    ready_to_assign: true,
    credential_present: false,
    requires_api_key: false,
    features: {
      tool_calls: true,
      thinking: true,
      prompt_cache: "automatic_prefix",
    },
    models: [],
  };

  it("reads model-level tool capability evidence", () => {
    expect(
      providerToolCallSupport({ ...base, models: [providerModel("m")] }),
    ).toBe("supported");
    expect(
      providerToolCallSupport({
        ...base,
        models: [
          providerModel("m", {
            capabilities: assignableModelCapabilities({
              tools: { state: "unsupported" },
            }),
          }),
        ],
      }),
    ).toBe("unsupported");
  });

  // Empty discovery results provide no capability evidence.
  it("reports unknown when there is no model list to read", () => {
    expect(
      providerToolCallSupport({
        ...base,
        ready_to_assign: false,
        discovery_status: "error",
        models: [],
      }),
    ).toBe("unknown");
  });

  it("reports unknown when a listed model never said either way", () => {
    expect(
      providerToolCallSupport({
        ...base,
        models: [
          providerModel("silent", {
            capabilities: assignableModelCapabilities({
              tools: { state: "unknown" },
            }),
          }),
        ],
      }),
    ).toBe("unknown");
    // An unknown model prevents a definitive unsupported result.
    expect(
      providerToolCallSupport({
        ...base,
        models: [
          providerModel("no", {
            capabilities: assignableModelCapabilities({
              tools: { state: "unsupported" },
            }),
          }),
          providerModel("silent", {
            capabilities: assignableModelCapabilities({
              tools: { state: "unknown" },
            }),
          }),
        ],
      }),
    ).toBe("unknown");
  });
});

describe("providerStatusLabel", () => {
  const configuredNotReady: ProviderMeta = {
    id: "compat",
    kind: "openai-compatible",
    base_url: "https://gpu.example.test/v1",
    configured: true,
    ready_to_assign: false,
    credential_present: true,
    requires_api_key: true,
    features: {
      tool_calls: true,
      thinking: false,
      prompt_cache: "none",
    },
    models: [],
  };

  it("never claims a connection that was only configured", () => {
    // `configured` means credentials are in place; nothing answered.
    expect(providerStatusLabel(configuredNotReady)).toBe(
      MODELS_SETTINGS_COPY.statusNotChecked,
    );
    expect(providerStatusLabel(configuredNotReady)).not.toBe("Connected");
  });

  it("reports the discovery axis when the host tried and failed", () => {
    expect(
      providerStatusLabel({ ...configuredNotReady, discovery_status: "error" }),
    ).toBe(MODELS_SETTINGS_COPY.statusCannotReach);
    expect(
      providerStatusLabel({ ...configuredNotReady, discovery_status: "empty" }),
    ).toBe(MODELS_SETTINGS_COPY.statusNoModels);
    expect(
      providerStatusLabel({
        ...configuredNotReady,
        discovery_status: "ok",
        models: [providerModel("m")],
      }),
    ).toBe(MODELS_SETTINGS_COPY.statusNoAssignableModel);
  });

  it("keeps readiness and the missing-key ask ahead of the reach question", () => {
    expect(
      providerStatusLabel({
        ...configuredNotReady,
        ready_to_assign: true,
        discovery_status: "error",
      }),
    ).toBe(MODELS_SETTINGS_COPY.statusReady);
    expect(
      providerStatusLabel({ ...configuredNotReady, credential_present: false }),
    ).toBe(MODELS_SETTINGS_COPY.statusNeedsApiKey);
    expect(
      providerStatusLabel({
        ...configuredNotReady,
        configured: false,
        requires_api_key: false,
      }),
    ).toBe(MODELS_SETTINGS_COPY.statusUnavailable);
  });
});

describe("providerDiscoveryFailure", () => {
  const failing: ProviderMeta = {
    id: "compat",
    kind: "openai-compatible",
    base_url: "https://gpu.example.test/v1",
    configured: true,
    ready_to_assign: false,
    credential_present: true,
    requires_api_key: true,
    discovery_status: "error",
    features: {
      tool_calls: true,
      thinking: false,
      prompt_cache: "none",
    },
    models: [],
  };

  it("is null unless the host reported a failed listing", () => {
    expect(
      providerDiscoveryFailure({ ...failing, discovery_status: "ok" }),
    ).toBeNull();
  });

  it("names the endpoint, says what is unknown, and gives a next step", () => {
    const failure = providerDiscoveryFailure(failing);
    expect(failure?.title).toBe(MODELS_SETTINGS_COPY.discoveryFailed);
    expect(failure?.detail).toContain("gpu.example.test");
    expect(failure?.detail).toContain("unknown");
    expect(failure?.nextStep).toBe(
      MODELS_SETTINGS_COPY.discoveryFailedNextStep,
    );
    expect(failure?.nextStep).toContain(MODELS_SETTINGS_COPY.testConnection);
  });

  it("uses the host's observed status axes for the next step", () => {
    expect(
      providerDiscoveryFailure({
        ...failing,
        requires_api_key: true,
        credential_present: false,
      })?.nextStep,
    ).toBe(MODELS_SETTINGS_COPY.discoveryFailedNextStepKeyMissing);
    expect(
      providerDiscoveryFailure({
        ...failing,
        configured: false,
      })?.nextStep,
    ).toBe(MODELS_SETTINGS_COPY.discoveryFailedNextStepEndpoint);
  });
});

describe("default model patches", () => {
  const ref = { provider_id: "openai", model: "gpt-4o-mini" };
  const summarizer = { provider_id: "ollama", model: "llama3.1" };

  it("defaultModelPatch writes coordinator and pool", () => {
    const patch = defaultModelPatch(ref);
    expect(patch.coordinator).toEqual(ref);
    expect(patch.agent_pool?.models).toEqual([ref]);
    expect(patch.lite).toBeUndefined();
  });

  it("summarizerPatch writes lite", () => {
    expect(summarizerPatch(summarizer)).toEqual({ lite: summarizer });
    expect(summarizerPatch({ provider_id: "", model: "" })).toEqual({ lite: null });
  });

  it("assignmentPatch clears unassigned slots", () => {
    const patch = assignmentPatch({
      coordinator: ref,
      lite: { provider_id: "", model: "" },
      agent_pool: { selection: "first", models: [ref] },
    });
    expect(patch).toEqual({
      coordinator: ref,
      lite: null,
      agent_pool: { selection: "first", models: [ref] },
    });
    expect(assignmentPatch({ agent_pool: { selection: "first", models: [] } }).coordinator).toBeNull();
  });

  it("providerRemovalPatch omits unchanged slots", () => {
    const mixed: ModelPolicy = {
      coordinator: { provider_id: "openai", model: "gpt-4o-mini" },
      lite: { provider_id: "ollama", model: "llama3.1" },
      agent_pool: {
        selection: "round_robin",
        models: [{ provider_id: "openai", model: "gpt-4o-mini" }],
      },
    };
    const patch = providerRemovalPatch(mixed, "openai");
    expect(patch?.coordinator).toBeNull();
    expect(patch?.agent_pool?.models).toEqual([]);
    expect(patch?.lite).toBeUndefined();
  });

  it("clears coordinator and pool when the default is unset", () => {
    const patch = defaultModelPatch({ provider_id: "", model: "" });
    expect(patch.coordinator).toBeNull();
    expect(patch.agent_pool?.models).toEqual([]);
    expect(patch.lite).toBeUndefined();
  });

  it("detects an unset default", () => {
    expect(hasDefaultModel(policy)).toBe(true);
    expect(hasDefaultModel(undefined)).toBe(false);
    expect(
      hasDefaultModel({ ...policy, coordinator: { provider_id: "", model: "" } }),
    ).toBe(false);
  });
});

describe("summarizer model (settings)", () => {
  it("reads the lite slot", () => {
    expect(summarizerOverrideRef(policy)).toEqual(policy.lite);
  });
});

describe("--- inherit / unset slots", () => {
  const emptyPolicy: ModelPolicy = {
    agent_pool: { selection: "first", models: [] },
  };

  it("labels empty refs as ---", () => {
    expect(formatModelAssignmentLabel({ provider_id: "", model: "" })).toBe(
      MODELS_SETTINGS_COPY.unsetModel,
    );
    expect(MODELS_SETTINGS_COPY.unsetModel).toBe("---");
  });

  it("copy describes summarizer inherit behavior", () => {
    expect(MODELS_SETTINGS_COPY.summarizerModelHint).toContain("inherits");
    expect(MODELS_SETTINGS_COPY.summarizerModelHint).toContain("tracks");
  });

  it("empty project-style policy has no assignments", () => {
    expect(policyHasModelAssignments(emptyPolicy)).toBe(false);
    expect(hasDefaultModel(emptyPolicy)).toBe(false);
    expect(summarizerOverrideRef(emptyPolicy)).toEqual({
      provider_id: "",
      model: "",
    });
  });

  it("addPoolRow appends an empty row", () => {
    const withDefault: ModelPolicy = {
      coordinator: { provider_id: "openai", model: "gpt-4o" },
      lite: { provider_id: "", model: "" },
      agent_pool: {
        selection: "first",
        models: [{ provider_id: "openai", model: "gpt-4o" }],
      },
    };
    const pooled = addPoolRow(withDefault);
    const models = pooled.agent_pool.models;
    expect(models[models.length - 1]).toEqual({
      provider_id: "",
      model: "",
    });
  });
});
