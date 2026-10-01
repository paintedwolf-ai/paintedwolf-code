/** User-facing AI providers settings copy. */

/** Named so guidance can point at the control by the label it actually wears. */
const TEST_CONNECTION_LABEL = "Test connection";
const REFRESH_MODELS_LABEL = "Refresh models";
const CONTINUE_LABEL = "Continue";

export const MODELS_SETTINGS_COPY = {
  providersIntro: "Connect AI providers.",
  addProvider: "Add AI provider",
  providersBack: "AI providers",
  providersEmpty: "No AI providers yet — add one to connect models.",
  newProviderHeading: "Add an AI provider",
  filterProviders: "Filter providers…",
  filterModels: "Filter models…",
  noFilterMatches: "No providers match.",
  noModelFilterMatches: "No models match.",
  cancel: "Cancel",
  providerLabelLabel: "Name (optional)",
  providerLabelPlaceholder: "e.g. GPU box, Work account",
  /** Post-add rename of ProviderMeta.label (display only; id stays fixed). */
  renameDisplayName: "Rename display name",
  providerLabelRenameHint:
    "Used in settings lists. The provider id stays the same for model assignment.",
  providerBaseUrlLabel: "Endpoint URL",
  cloudflareAccountIdLabel: "Cloudflare account ID",
  cloudflareAccountIdHint:
    "Copy your account ID from Cloudflare → Workers AI → Use REST API. The endpoint URL updates as you type.",
  cloudflareAccountIdRequired: "Enter your Cloudflare account ID to complete the endpoint.",
  cloudflareEndpointInvalid: "Enter a URL containing /accounts/your-account-id/ai/v1.",
  cloudflareModelAccessHint:
    "Some listed models require a Workers Paid plan or prepaid AI Gateway credits. Listing a model does not confirm your account can run it.",
  providerRegionLabel: "Region",
  providerDerivedEndpointHint:
    "The endpoint is derived from the ambient credential configuration.",
  providerDeploymentsLabel: "Deployment names",
  providerDeploymentsPlaceholder:
    "chat-prod=gpt-5.6-sol, summarizer=gpt-5.4-mini",
  providerDeploymentsHint:
    "Enter Azure deployment names, separated by commas. Use deployment=model to attach catalog pricing.",
  saveDeployments: "Save deployments",
  /** Chooses between a stored key and a kind's ambient credential chain. */
  providerCredentialLabel: "Credential",
  providerApiKeyLabel: "API key",
  providerApiKeyPlaceholder: "Paste your API key…",
  /** Shown under the add-form key field — key is optional at add time. */
  providerApiKeyOptionalHint: "Optional — you can also paste a key after adding.",
  /** Bullet mask — makes a stored key read as filled without revealing it. */
  providerApiKeyPlaceholderStored: "••••••••••••••••",
  apiKeyStoredStatus: "Key saved",
  apiKeyMissingStatus: "No key saved",
  /** Resolution readout for an instance in ambient credential mode. */
  ambientCredentialsActive: "Ambient credentials detected.",
  ambientCredentialsMissing: "Ambient credentials not detected yet.",
  /** When the provider last accepted the saved credentials (ProviderMeta.credential_verified_at). */
  observedCredentialsAccepted: (checkedAt?: string) => {
    const d = checkedAt ? new Date(checkedAt) : null;
    return d && !Number.isNaN(d.getTime())
      ? `Credentials verified ${d.toLocaleString(undefined, {
          dateStyle: "medium",
          timeStyle: "short",
        })}`
      : "Credentials verified";
  },
  saveApiKey: "Save key",
  clearApiKey: "Clear key",
  /** Device-scope decision to send detected credentials to one provider without a card. */
  secretScreenTrustLabel: "Trust this provider with secrets",
  secretScreenTrustHint:
    "Nothing will ask before a detected secret is sent to this endpoint. Meant for a model running on this machine. Transcripts are still redacted, and changing the endpoint turns this off.",
  secretScreenTrustLoopbackNote:
    "This endpoint is on this machine, though a local proxy can still forward requests elsewhere.",
  secretScreenTrustRemoteNote: "This endpoint is not on this machine.",
  removeProvider: "Remove AI provider",
  instanceCount: (n: number) => `${n} instance${n === 1 ? "" : "s"}`,
  usageQuota: (used: number, limit: number, unit: string) =>
    `Daily ${unit}: ${formatQuotaNumber(used)} / ${formatQuotaNumber(limit)} free`,
  usageQuotaOverage: (usd: number) =>
    `Est. overage today: $${usd.toFixed(4)}`,
  /**
   * Tool-call support is a tri-state, and each state gets its own sentence.
   * "Can't" is only ever said when every listed model said so.
   */
  noToolCallsWarning:
    "This AI provider can't make tool calls, so its models can't run agent actions like reading or editing files.",
  toolCallsUnknownNoModels: `No models have been listed for this AI provider yet, so whether it can make tool calls is unknown. Use ${REFRESH_MODELS_LABEL} or ${TEST_CONNECTION_LABEL} to find out.`,
  toolCallsUnknownFilteredModels: `The provider returned model IDs, but none have the metadata needed for chat. Check the provider's model configuration, then use ${REFRESH_MODELS_LABEL}. Tool-call support is still unknown.`,
  toolCallsUnknownEmptyModels: `The provider returned an empty model list. Check which models are available at this endpoint, then use ${REFRESH_MODELS_LABEL}. Tool-call support is still unknown.`,
  toolCallsUnknownNoEvidence: `None of this AI provider's listed models say whether they support tool calls, so it is unknown. Use ${REFRESH_MODELS_LABEL} to read the list again.`,
  saveEndpoint: "Save endpoint",
  testConnection: TEST_CONNECTION_LABEL,
  testConnectionScopeHint: "Checks the connection and model list without running a model.",
  testConnectionPassed: "Connected; model access not verified",
  testingConnection: "Testing…",
  refreshModels: REFRESH_MODELS_LABEL,
  refreshingModels: "Refreshing…",
  /** Retries the model-list refresh after it failed; the saved list stays put. */
  retryRefreshModels: "Retry",
  modelsFound: (n: number) => `${n} model${n === 1 ? "" : "s"} found`,
  defaultModelHint:
    "Must be tool-calling capable — Painted Wolf Code cannot run without it. We recommend GLM 5.3 Flash, Gemini 3.5 Flash, Claude Sonnet, or a similar class model.",
  /** Shorter hints for the first-run onboarding gate. */
  onboardingProvidersIntro:
    "The coordinator needs a high quality model, similar to GLM or better. Smaller models may struggle to successfully call tools and understand what to do.",
  onboardingContinue: CONTINUE_LABEL,
  onboardingSkip: "Skip",
  onboardingBack: "Back",
  onboardingContinueValidating: "Checking connection…",
  /** Identifies the missing connection requirement. */
  onboardingContinueNeedProvider: `Add an AI provider and save its key — ${CONTINUE_LABEL} unlocks once one is ready.`,
  onboardingContinueNeedDefaultModel: `Choose a default model — ${CONTINUE_LABEL} unlocks once one is set.`,
  /** Names the providers region for screen readers; the visible title is the page heading. */
  onboardingProvidersHeading: "AI providers",
  onboardingValidationFailed: "Could not verify the provider. Check the endpoint and key, then try again.",
  /** Cancels the connection check while allowing onboarding to continue. */
  onboardingCancelCheck: "Stop checking",
  /** Failed or cancelled checks can continue with the saved credentials. */
  onboardingContinueAnyway: "Continue anyway",
  onboardingSetupTitle: "Connect a model",
  onboardingSetupLede:
    "Add a provider and choose a default model. You can change this later.",
  onboardingSummarizerTitle: "Add a summarizer",
  onboardingSummarizerOptional: "Optional",
  onboardingSummarizerLede:
    "Optional second model for smaller tasks.",
  onboardingSummarizerLocalSaveHint:
    "A local model for summarizing can save money while your default model handles the hard work.",
  onboardingSummarizerAddProvider: "Add another provider",
  onboardingSummarizerAddProviderHint:
    "If you want the summarizer to use a different provider.",
  onboardingSummarizerHideProvider: "Hide",
  onboardingWelcomeTitle: "You're ready",
  onboardingWelcomeBody:
    "You can always change your AI providers later.",
  onboardingWelcomeSettingsHint: "AI providers live here",
  onboardingWelcomeSettingsHintSub: "Change them anytime",
  onboardingWelcomeDismiss: "Get started",
  // Connection status and recovery actions.
  onboardingWaitingForEngine: "Still getting ready…",
  onboardingWaitingForEngineHint:
    "You can skip this setup and add AI providers later from Settings.",
  // The missing-provider card on Home and in the chat dock: no provider, or no
  // default model (notices/no-provider-card.ts).
  noProviderTitle: "Add an AI provider",
  noProviderBody:
    "Nothing can answer your prompts yet.",
  noProviderCta: "Add a provider",
  noDefaultModelTitle: "Choose a default model",
  noDefaultModelBody:
    "A provider is ready, but no model is set to answer by default.",
  noDefaultModelCta: "Choose a model",
  modelPolicyIntro:
    "Choose which models handle coordination, summarization, and delegated workers. Only ready AI providers are listed.",
  catalogUnavailable: "Catalog unavailable",
  catalogStale: "Catalog stale",
  /** Connection testing reports the provider error omitted from model listings. */
  discoveryFailed: "Could not list models",
  discoveryFailedDetail: (endpoint: string) =>
    endpoint
      ? `The last attempt to list models from ${endpoint} did not finish, so this provider's models and their capabilities are unknown.`
      : "The last attempt to list models did not finish, so this provider's models and their capabilities are unknown.",
  discoveryFailedNextStepKeyMissing: `No API key is saved yet. Add one, then use ${TEST_CONNECTION_LABEL} to see what the provider says.`,
  discoveryFailedNextStepEndpoint: `The endpoint was rejected before the request went out. Check it, then use ${TEST_CONNECTION_LABEL} to see what the provider says.`,
  discoveryFailedNextStep: `Check the endpoint and key, then use ${TEST_CONNECTION_LABEL} to see what the provider says.`,
  discoveryEmpty: "No live models",
  /** Provider list and card badges — each states something the host established. */
  statusReady: "Ready",
  statusNeedsSetup: "Needs setup",
  statusNeedsApiKey: "Needs API key",
  statusCannotReach: "Can't reach",
  statusNoModels: "No models",
  statusNoAssignableModel: "No model to assign",
  /** Credentials are in place and nothing has been reached yet. */
  statusNotChecked: "Not checked",
  statusUnavailable: "Unavailable",
  coordinatorSlotLabel: "Coordinator",
  coordinatorSlotHint:
    "Must be tool-calling capable or Painted Wolf Code cannot run. We recommend GLM 5.3 Flash, Gemini 3.5 Flash, Claude Sonnet, or a similar class model — this is the hard part, so use your strongest model.",
  liteSlotLabel: "Summarizer",
  summarizerModelHint:
    "Optional override for compaction, context diet, and web search seeding. --- inherits the default model and tracks when it changes.",
  modelPolicyEmpty:
    "Configure at least one provider in Settings → AI providers, then choose models here.",
  workerSectionTitle: "Workers",
  workerSectionHint:
    "Delegated turns that explore the workspace and apply edits. Use one model, or enable a pool below to spread work across several.",
  workerSlotLabel: "Worker",
  workerSlotHint:
    "Build, exploration, and other delegated turns the coordinator dispatches.",
  workerPoolToggle: "Use a randomized pool of workers",
  workerPoolIntro:
    "Add two or more models. Each delegated worker draws from this pool.",
  workerPoolListHeader: "Model",
  workerPoolAddLabel: "Add model",
  workerPoolSelectionLabel: "When one worker runs",
  workerPoolSelectionHint:
    "How a single delegated worker picks from the pool when only one runs.",
  workerPoolHint:
    "When multiple workers run together, Painted Wolf Code picks as different models as possible from your pool.",
  /** Prefs-band label — short; onboarding keeps the longer field label via copy sites. */
  defaultSummarizerModelLabel: "Summarizer",
  unsetModel: "---",
  defaultModelRequired:
    "You must set up a default model before using Painted Wolf Code.",
  noProvidersLoaded: "No providers loaded from the backend.",
  selectModelFirst: "Set up a provider first",
  /** Provider is configured, but every one of its models is filtered out for this role. */
  allModelsFilteredForRole: "No models fit this role",
  allModelsFilteredForRoleHint:
    "Your providers offer models, but none of them qualify for this role. Turn on Show all models in Settings → Advanced to assign one anyway.",
  providerGroupLocal: "Run locally",
  providerGroupCloud: "Cloud / hosted",
  providerGroupCustom: "Custom",
  providerRequireApiKey: "Requires an API key",
} as const;

function formatQuotaNumber(n: number): string {
  if (n >= 10_000) {
    return `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}k`;
  }
  return Math.round(n).toLocaleString();
}
