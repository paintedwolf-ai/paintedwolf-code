# First run

The hard gate a new install shows before the home stage, and how a missing provider is reported once that gate is done.

**Implementation:** [`components/onboarding/`](../lycaon-den/src/components/onboarding) — `OnboardingGate.tsx`, `onboarding-gate-model.ts` (page order and the show / page / continue / layout predicates), `OnboardingStepPage.tsx`, `OnboardingLayoutChoices.tsx` and `OnboardingLayoutPreview.tsx` (the starting layout page). Styles: [`onboarding-domain.css`](../lycaon-den/src/onboarding-domain.css). Post-latch recovery: [`notices/no-provider-card.ts`](../lycaon-den/src/notices/no-provider-card.ts). Den persists `firstRunSetupCompleted` and `localModelOnboardingCompleted` in `AppState.onboarding`.

**See also:** [Providers](providers.md) · [Den](den.md#notifications) · [Naming](naming.md)

---

## The gate

Fresh installations from normal downloads open on local model selection. Normal application downloads contain no generation weights. The host detects available hardware locally and recommends a tier. Setup preselects that tier, and the user can choose another eligible model, import a signed offline bundle, or continue without a local model. A transfer begins only after an explicit choice.

| Page | Choice |
|------|--------|
| **Local model** | Choose a model for this device. The page shows transfer progress and offers cancellation. |
| **Starting layout** | Open to the conversation alone or with a context view beside it. |
| **Welcome** | Find additional providers in Settings → AI providers and enter the workspace. |

An offline installer includes the selected model. The host imports it locally, and fresh setup starts at the layout page, including when the seed needs repair. Previously configured installations skip the model-only gate. Existing model choices and opt-outs survive. Settings offers recovery when the packaged model cannot load.

After a model decision, interrupted fresh setup resumes at layout. An existing normal installation sees just the local model page once. Returning users keep their existing provider configuration. Settings → AI providers remains available for later model downloads, offline imports, replacement, and removal.

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
