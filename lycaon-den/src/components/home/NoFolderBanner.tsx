import { SystemNudge } from "../SystemNudge.tsx";

type Props = {
  onAddFolder: () => void;
  onDismiss: () => void;
};

/** Notification-stack nudge when a non-draft project has no folder agents use. */
export function NoFolderBanner(props: Props) {
  return (
    <SystemNudge
      testId="nofolder-banner"
      title="No folder for the assistant"
      description="Add a folder so the assistant can read and edit your code."
      secondaryAction={{
        label: "Not now",
        testId: "nofolder-dismiss",
        onClick: () => props.onDismiss(),
      }}
      primaryAction={{
        label: "Add folder",
        testId: "nofolder-add",
        onClick: () => props.onAddFolder(),
      }}
      onDismiss={() => props.onDismiss()}
    />
  );
}
