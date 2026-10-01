import type { JSX } from "solid-js";
import {
  saveStartupCompanion,
  startupCompanionPref,
  workspaceOrientationPref,
  splitOrderPref,
} from "../../shell/layout-store.ts";
import { DenRadioControl } from "../primitives/DenRadio.tsx";
import { stageLabelFor } from "../stage/stage-registry.tsx";
import {
  onboardingLayoutChoice,
  onboardingSplitCompanion,
  startupCompanionForChoice,
  type OnboardingLayoutChoice,
} from "./onboarding-gate-model.ts";
import { ONBOARDING_LAYOUT_COPY } from "./onboarding-layout-copy.ts";
import { OnboardingLayoutPreview } from "./OnboardingLayoutPreview.tsx";

const RADIO_NAME = "onboarding-layout";

type OptionProps = {
  choice: OnboardingLayoutChoice;
  checked: boolean;
  name: string;
  description: string;
  preview: JSX.Element;
  onChoose: (choice: OnboardingLayoutChoice) => void;
};

function LayoutOption(props: OptionProps) {
  const nameId = `onboarding-layout-${props.choice}-name`;
  const descriptionId = `onboarding-layout-${props.choice}-description`;
  return (
    <label
      class="onboarding-layout__option"
      data-testid={`onboarding-layout-${props.choice}`}
    >
      <span class="onboarding-layout__art" aria-hidden="true">
        {props.preview}
      </span>
      <span class="onboarding-layout__label">
        <DenRadioControl
          name={RADIO_NAME}
          value={props.choice}
          checked={props.checked}
          aria-labelledby={nameId}
          aria-describedby={descriptionId}
          data-testid={`onboarding-layout-${props.choice}-radio`}
          onChange={(event) => {
            if (event.currentTarget.checked) props.onChoose(props.choice);
          }}
        />
        <span>
          <span class="onboarding-layout__name" id={nameId}>
            {props.name}
          </span>
          <span class="onboarding-layout__desc" id={descriptionId}>
            {props.description}
          </span>
        </span>
      </span>
    </label>
  );
}

/**
 * Open at launch as two cards. A pick saves at once, like the Settings row,
 * so Back and Continue only move between pages.
 */
export function OnboardingLayoutChoices(props: { labelledBy: string }) {
  const choice = () => onboardingLayoutChoice(startupCompanionPref());
  const splitCompanion = () => onboardingSplitCompanion(startupCompanionPref());
  const splitLabel = () => stageLabelFor(splitCompanion());
  const mirrored = () => workspaceOrientationPref() === "mirrored";
  const choose = (next: OnboardingLayoutChoice) => {
    void saveStartupCompanion(
      startupCompanionForChoice(next, startupCompanionPref()),
    );
  };

  return (
    <fieldset
      class="onboarding-layout"
      aria-labelledby={props.labelledBy}
      data-testid="onboarding-layout-choices"
    >
      <LayoutOption
        choice="chat"
        checked={choice() === "chat"}
        name={ONBOARDING_LAYOUT_COPY.chatName}
        description={ONBOARDING_LAYOUT_COPY.chatDescription}
        preview={<OnboardingLayoutPreview kind="chat" mirrored={mirrored()} />}
        onChoose={choose}
      />
      <LayoutOption
        choice="split"
        checked={choice() === "split"}
        name={ONBOARDING_LAYOUT_COPY.splitName(splitLabel())}
        description={ONBOARDING_LAYOUT_COPY.splitDescription(
          splitCompanion(),
          splitLabel(),
        )}
        preview={<OnboardingLayoutPreview kind="split" mirrored={mirrored()} chatFirst={splitOrderPref() === "chat-first"} />}
        onChoose={choose}
      />
    </fieldset>
  );
}
