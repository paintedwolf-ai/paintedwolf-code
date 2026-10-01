import { MODELS_SETTINGS_COPY } from "../settings/providers/models-settings-copy.ts";
import type { ProviderGap } from "../notices/no-provider-card.ts";
import { SystemNudge } from "./SystemNudge.tsx";

type Props = {
  gap: ProviderGap;
  onOpenProviders: () => void;
  onDismiss: () => void;
};

/** Missing provider or default model notification banner. */
export function NoProviderCard(props: Props) {
  const copy = () =>
    props.gap === "no_default_model"
      ? {
          title: MODELS_SETTINGS_COPY.noDefaultModelTitle,
          body: MODELS_SETTINGS_COPY.noDefaultModelBody,
          cta: MODELS_SETTINGS_COPY.noDefaultModelCta,
        }
      : {
          title: MODELS_SETTINGS_COPY.noProviderTitle,
          body: MODELS_SETTINGS_COPY.noProviderBody,
          cta: MODELS_SETTINGS_COPY.noProviderCta,
        };

  return (
    <SystemNudge
      testId="no-provider-banner"
      role="status"
      title={copy().title}
      description={copy().body}
      primaryAction={{
        label: copy().cta,
        testId: "no-provider-open-settings",
        onClick: () => props.onOpenProviders(),
      }}
      onDismiss={() => props.onDismiss()}
    />
  );
}
