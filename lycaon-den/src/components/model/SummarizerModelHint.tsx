import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";

/** The summarizer hint, naming the unset label as one unbroken token the way the picker shows it. */
export function SummarizerModelHint() {
  return (
    <>
      {MODELS_SETTINGS_COPY.summarizerModelHintLead}{" "}
      <span class="whitespace-nowrap" data-testid="summarizer-unset-label">
        {MODELS_SETTINGS_COPY.unsetModel}
      </span>{" "}
      {MODELS_SETTINGS_COPY.summarizerModelHintTail}
    </>
  );
}
