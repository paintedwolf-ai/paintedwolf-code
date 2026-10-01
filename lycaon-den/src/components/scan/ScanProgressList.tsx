import type { SecurityFullPass, SecurityOverview } from "../../api/types.ts";
import {
  fullPassMemberMeasured,
  fullPassMemberRatio,
  fullPassMemberReadout,
  fullPassMemberStatus,
  fullPassWaitingOn,
} from "../../lib/scan-coverage.ts";
import { scannerJobLabel, scannerJobPhrase } from "../../settings/extensions/scanners-catalog-model.ts";
import { ProgressRows, type ProgressRow } from "../primitives/ProgressRows.tsx";

type Props = {
  pass: SecurityFullPass;
  overview: SecurityOverview | null;
  testId?: string;
};

type PassMember = SecurityFullPass["members"][number];

function scannerIdentity(overview: SecurityOverview | null, member: PassMember) {
  return overview?.scanners.find((candidate) => candidate.id === member.scanner_id) ?? {
    id: member.scanner_id,
    label: "Scanner",
    categories: member.scan?.categories ?? [],
  };
}

function scannerPhrase(overview: SecurityOverview | null, member: PassMember | undefined): string {
  if (!member) return "another scanner";
  const scanner = scannerIdentity(overview, member);
  return scanner.label === "Scanner" && scanner.categories.length === 0
    ? "another scanner"
    : scannerJobPhrase(scanner);
}

/** Pending scanners retain their rows throughout the full pass. */
export function ScanProgressList(props: Props) {
  const rows = (): ProgressRow[] => {
    const waitingOn = fullPassWaitingOn(props.pass, (id) =>
      scannerPhrase(props.overview, props.pass.members.find((member) => member.scanner_id === id)),
    );
    return props.pass.members.map((member) => ({
      key: member.scanner_id,
      label: scannerJobLabel(scannerIdentity(props.overview, member)),
      status: fullPassMemberStatus(member),
      ratio: fullPassMemberRatio(member),
      measured: fullPassMemberMeasured(member),
      readout: fullPassMemberReadout(member, waitingOn),
      state: member.phase,
    }));
  };
  return (
    <ProgressRows
      rows={rows()}
      testId={props.testId ?? "scans-full-scan-progress"}
      statusTestId="scans-full-scan-status"
      readoutTestId="scans-full-scan-readout"
    />
  );
}
