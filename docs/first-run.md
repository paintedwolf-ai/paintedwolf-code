# First run

The hard gate a new install shows before the home stage, and how a missing provider is reported once that gate is done.

**Implementation:** [`components/onboarding/`](../lycaon-den/src/components/onboarding) — `OnboardingGate.tsx`, `onboarding-gate-model.ts` (page order and the show / page / continue / layout predicates), `OnboardingStepPage.tsx`, `OnboardingLayoutChoices.tsx` and `OnboardingLayoutPreview.tsx` (the starting layout page). Styles: [`onboarding-domain.css`](../lycaon-den/src/onboarding-domain.css). Post-latch recovery: [`notices/no-provider-card.ts`](../lycaon-den/src/notices/no-provider-card.ts). Den persists `completedSetupRevision` and `completedLocalModelStepRevision` in `AppState.onboarding`.

**See also:** [Providers](providers.md) · [Den](den.md#notifications) · [Naming](naming.md)

---

## The gate

Fresh installations from normal downloads open on local model selection when the host has an eligible model download and no recorded selection or opt-out. Normal application downloads contain no generation weights. The host detects available hardware locally and recommends a tier. Setup preselects that tier when published. If another eligible tier has a download, setup can preselect it instead. The user can choose another eligible model, import a signed offline bundle, or continue without a local model. Each tier shows its resource tradeoff and download size. The download button starts the transfer. Setup shows the target model, bytes received, and activation progress before it continues. Canceling keeps setup open so the user can retry or choose another tier. If model availability cannot be read, setup offers another check. During a transfer or after a failure, the user can continue setup and manage models later in Settings. Leaving the page preserves the host’s model choice and any active transfer.

| Page | Choice |
|------|--------|
| **Local model** | Choose a model for this device. The page shows transfer progress and offers cancellation. |
| **Starting layout** | Open to the conversation alone or with a context view beside it. |
| **Welcome** | Find additional providers in Settings → AI providers and enter the workspace. |

An offline installer includes the selected model. The host imports it locally, and fresh setup starts at the layout page, including when the seed needs repair. Installations with a recorded model decision skip the local model page. Existing model choices and opt-outs survive. Settings offers recovery when the packaged model cannot load.

When the host reports no eligible downloadable tier, fresh setup starts at layout and returning users enter the workspace. Skipping the page preserves the model decision for a later release that offers a download. A model imported in Settings or through the CLI satisfies the model step, as does a recorded opt-out. Setup honors that decision even when the runtime needs repair. Users can import a signed bundle at any time in Settings → AI providers.

Everyone upgrading from a pre-1.1 installation sees the current walkthrough once. It follows the same host facts as a fresh install and opens the layout page with the saved layout selected. Provider configuration and model assignments are preserved. After the model decision, interrupted setup resumes at layout. Completing welcome records the walkthrough revision. If someone postponed the model decision, a later launch can offer just the model page when an eligible download is available. Escape can dismiss setup without recording a model decision. Settings → AI providers remains available for later model downloads, offline imports, replacement, and removal.

### Starting layout

The page is the existing **Settings › General › Open at launch** row (`layout.startupCompanion`) drawn as two cards. It owns no preference of its own, including the column a narrow split keeps, which follows this choice until Settings pins one ([`den.md`](den.md#workspace-model)):

| Card | Open at launch | Stored |
|------|----------------|--------|
| **Chat** | Chat only | unset |
| **Files and chat** | Split with Files | `files` |

Chat is the default because an unset value already means it. A pick saves immediately through `saveStartupCompanion`, exactly as the Settings row does, so the page and the row cannot disagree. When first run returns after a store reset with another companion already set, the split card names that companion instead of replacing it with Files (`onboardingSplitCompanion`). The other companions stay in Settings.

The cards are native radios in a `fieldset` labelled by the page title, so arrow keys move between them. Each card shows a miniature window drawn from the active theme's tokens and mirrored when the workspace orientation is mirrored; the previews are decorative and hidden from assistive technology, and the name and description label each radio.

The launch layout is spent once per run, when the first conversation is live. It is held while first run is on screen (`startupPlacementDecision`'s `firstRunPending`), so a chat restored behind the gate cannot spend it before the person has chosen. A split needs a project, so on a fresh install it applies when the first project chat opens.

---

## After the latch

A configuration gap after first run is not a second gate. It is the condition the host's `NO_PROVIDER_CONFIGURED` readiness probe reports, surfaced as one non-dismissible card shared by Home and the chat dock. `providerConfigGap` returns which half is missing, because someone with no provider needs an API key while someone who has one only needs to choose a model. The card is suppressed until the sidecar is connected and models are checked, so a cold start cannot flash it.

`CriticalStop` also replaces the stage, but it reports an unreachable backend rather than an unconfigured one.

Coverage: `onboarding-gate-model.test.ts`, `OnboardingGate.test.tsx`, `stage-placement.test.ts` (the launch layout held through first run), and `e2e/onboarding-first-run.spec.ts`.
