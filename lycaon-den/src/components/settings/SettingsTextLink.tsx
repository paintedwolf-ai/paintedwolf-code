import type { JSX } from "solid-js";

type Props = {
  onClick: () => void;
  children: JSX.Element;
  testId?: string;
};

/** Inline jump inside settings prose — same chrome as SettingsScopeLede. */
export function SettingsTextLink(props: Props) {
  return (
    <button
      type="button"
      class="den-settings-scope-lede__link"
      data-testid={props.testId}
      onClick={() => props.onClick()}
    >
      <span>{props.children}</span>
    </button>
  );
}
