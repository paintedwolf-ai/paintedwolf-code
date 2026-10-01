import { vi } from "vitest";
import { render } from "@solidjs/testing-library";
import type { ProviderMeta } from "../api/types.ts";
import { createSettingsStore } from "../store/settings-store.ts";
import { providerModel } from "./provider-fixtures.ts";
import { ProvidersPanel } from "../components/settings/providers/ProvidersPanel.tsx";

export const providers: ProviderMeta[] = [
  {
    id: "openai",
    kind: "openai",
    label: "OpenAI",
    base_url: "https://api.openai.com/v1",
    configured: true,
    ready_to_assign: true,
    credential_present: true,
    requires_api_key: true,
    features: {
      tool_calls: true,
      thinking: true,
      prompt_cache: "automatic_prefix",
    },
    models: [providerModel("gpt-4o")],
  },
  {
    id: "fireworks",
    kind: "fireworks",
    label: "Fireworks",
    base_url: "https://api.fireworks.ai/inference/v1",
    configured: false,
    ready_to_assign: false,
    credential_present: false,
    requires_api_key: true,
    features: {
      tool_calls: true,
      thinking: true,
      prompt_cache: "automatic_prefix",
    },
    models: [],
  },
  {
    id: "ollama",
    kind: "ollama",
    label: "Ollama",
    base_url: "http://localhost:11434/v1",
    configured: true,
    ready_to_assign: true,
    credential_present: false,
    requires_api_key: false,
    features: {
      tool_calls: true,
      thinking: true,
      prompt_cache: "local_kv",
    },
    models: [providerModel("llama3.1")],
  },
];

export const kinds = [
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
  {
    kind: "ollama",
    label: "Ollama",
    base_url: "http://localhost:11434/v1",
    requires_api_key: false,
  },
];

export const bedrockKind = {
  kind: "bedrock",
  label: "Amazon Bedrock",
  base_url: "us-east-1",
  endpoint_style: "region",
  requires_api_key: true,
  ambient_auth: "aws-sdk-chain",
};

export const vertexKind = {
  kind: "vertex",
  label: "Google Vertex AI",
  base_url: "",
  endpoint_style: "derived",
  requires_api_key: false,
  ambient_auth: "google-adc",
};

export const vertexExpressKind = {
  kind: "vertex-express",
  label: "Google Vertex AI (express mode)",
  base_url: "https://aiplatform.googleapis.com/v1",
  endpoint_style: "url",
  requires_api_key: true,
};

export function bedrockProvider(overrides: Partial<ProviderMeta> = {}): ProviderMeta {
  return {
    id: "bedrock",
    kind: "bedrock",
    label: "Amazon Bedrock",
    base_url: "us-east-1",
    configured: true,
    ready_to_assign: true,
    credential_present: true,
    requires_api_key: true,
    ambient_auth: "aws-sdk-chain",
    features: {
      tool_calls: true,
      thinking: false,
      prompt_cache: "none",
    },
    models: [providerModel("us.anthropic.claude-sonnet-4-5-20250929-v1:0")],
    ...overrides,
  };
}

export function renderCredentialModeCard(
  provider: ProviderMeta,
  kindList: unknown[],
  updateProvider = vi.fn().mockResolvedValue({}),
) {
  const settingsStore = createSettingsStore({ providers: [provider] });
  const utils = render(() => (
    <ProvidersPanel
      client={
        {
          listProviders: vi.fn().mockResolvedValue([provider]),
          listProviderKinds: vi.fn().mockResolvedValue(kindList),
          updateProvider,
          replaceProviderCredential: vi.fn(),
          deleteProviderCredential: vi.fn(),
          deleteProvider: vi.fn(),
          testProvider: vi.fn(),
          refreshProviderModels: vi.fn(),
        } as never
      }
      settingsStore={settingsStore}
      providers={[provider]}
      onRemoveProvider={() => undefined}
    />
  ));
  return { ...utils, updateProvider };
}
