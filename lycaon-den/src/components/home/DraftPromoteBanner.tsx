import { SystemNudge } from "../SystemNudge.tsx";

type Props = {
  onPromote: () => void;
  onDismiss: () => void;
};

/** Notification-stack nudge offering to keep a draft as a saved project once work is underway. */
export function DraftPromoteBanner(props: Props) {
  return (
    <SystemNudge
      testId="draft-promote-banner"
      title="Save to a folder"
      description="Choose where to keep this project on disk."
      secondaryAction={{
        label: "Not yet",
        testId: "draft-promote-dismiss",
        onClick: () => props.onDismiss(),
      }}
      primaryAction={{
        label: "Save to folder…",
        testId: "draft-promote-save",
        onClick: () => props.onPromote(),
      }}
      onDismiss={() => props.onDismiss()}
    />
  );
}
