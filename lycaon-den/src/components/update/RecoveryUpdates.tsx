import type { UpdateService } from "../../settings/system/update-service.ts";
import { UpdatePanel } from "./UpdatePanel.tsx";

export function RecoveryUpdates(props: { updateService?: UpdateService }) {
  return (
    <details class="den-recovery-updates" data-testid="recovery-updates">
      <summary><span class="den-disclosure-caret" aria-hidden="true" />Update Painted Wolf Code</summary>
      <p>A newer release may resolve this startup problem. Updating preserves your saved data.</p>
      <UpdatePanel updateService={props.updateService} />
    </details>
  );
}
