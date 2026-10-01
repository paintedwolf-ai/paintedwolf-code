import {
  APP_SETTINGS,
  type SettingsSection,
} from "../../settings/settings-nav-model.ts";
import { SettingsSectionList } from "./SettingsSectionList.tsx";

type Props = {
  section: SettingsSection;
  onSectionChange: (section: SettingsSection) => void;
};

/** App-level settings section picker nested under the Settings nav link. */
export function SettingsNavSidebar(props: Props) {
  return (
    <SettingsSectionList
      items={APP_SETTINGS}
      active={props.section}
      ariaLabel="Settings sections"
      testidPrefix="settings-nav"
      onSelect={(id) => props.onSectionChange(id as SettingsSection)}
    />
  );
}
