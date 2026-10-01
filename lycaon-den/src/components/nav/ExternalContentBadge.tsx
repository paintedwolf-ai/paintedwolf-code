import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import {
  EXTERNAL_CONTENT_BADGE_LABEL,
  EXTERNAL_CONTENT_BADGE_TITLE,
} from "../../chat/untrusted/untrusted-content-copy.ts";

type Props = {
  visible: boolean;
  /** Opens session-scoped search for external ingest triggers. */
  onActivate?: () => void;
};

/** Composer-pad icon when Session.untrusted_content is true. */
export function ExternalContentBadge(props: Props) {
  if (!props.visible) return null;
  return (
    <button
      type="button"
      class="den-composer-status-button"
      data-testid="external-content-badge"
      aria-label={EXTERNAL_CONTENT_BADGE_LABEL}
      data-tip={EXTERNAL_CONTENT_BADGE_TITLE}
      onClick={() => props.onActivate?.()}
    >
      <ThemeIcon slot="external-content" size={14} />
    </button>
  );
}
