import type { Accessor } from "solid-js";
import type { SecurityFinding } from "../../api/types.ts";
import { offerViewFacet, type ViewFacet } from "../../shell/view-report.ts";

/** The host refuses longer identities, which would withdraw the whole window's report. */
const MAX_IDENTITY = { rule: 128, fingerprint: 256 };

/** The finding a security pane shows, located in its scanned folder when the location is relative. */
export function securityViewFacet(rootId: string | undefined, finding: SecurityFinding | null | undefined): ViewFacet {
  if (!finding || finding.rule_id.length > MAX_IDENTITY.rule || finding.fingerprints.primary.length > MAX_IDENTITY.fingerprint) return {};
  const location = finding.locations[0];
  const relative = location && rootId && !/^[a-z][a-z0-9+.-]*:/i.test(location.uri) && !location.uri.startsWith("/");
  return {
    findings: [{
      fingerprint: finding.fingerprints.primary,
      rule_id: finding.rule_id,
      level: finding.level,
      location: relative ? { root_id: rootId, path: location.uri, ...(location.start_line ? { line: location.start_line } : {}) } : undefined,
    }],
  };
}

export function offerSecurityViewFacet(deps: {
  projectId: Accessor<string | undefined>;
  rootId: Accessor<string | undefined>;
  finding: Accessor<SecurityFinding | null | undefined>;
}): void {
  offerViewFacet(deps.projectId, () => securityViewFacet(deps.rootId(), deps.finding()));
}
