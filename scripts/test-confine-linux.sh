#!/usr/bin/env bash
# Applied Linux confinement on this host's architecture: the confine live suites, the command
# supervisor, pinned browser, and native helper boundaries. Each leg runs even when an earlier one fails.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
# User-wide like the other pinned tools, so a lane can provision it before it goes offline.
BROWSER_CACHE="${PW_BIN_DIR}/confine-browser"

LEGS=(sandbox supervisor launch browser helpers)
AVAILABLE_LEGS=("${LEGS[@]}" launch-desktop)
# --prepare provisions the pinned browser and runs nothing, for a lane that later runs offline.
PREPARE=0
if [[ "${1:-}" == "--prepare" ]]; then
  PREPARE=1
  shift
fi
if (($#)); then
  for leg in "$@"; do
    [[ " ${AVAILABLE_LEGS[*]} " == *" ${leg} "* ]] || { echo "error: unknown leg ${leg}; known: ${AVAILABLE_LEGS[*]}" >&2; exit 2; }
  done
  LEGS=("$@")
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "test:confine:linux: unverified: Linux confinement runs on Linux only" >&2
  exit 2
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# Prints the pinned browser's path, downloading it into the cache when it is missing.
browser_path() {
  local out
  (cd "${ROOT}/lycaon" && go build -o "${WORK}/pw" ./cmd/lycaon) || return 1
  out="$("${WORK}/pw" browser ensure --cache-dir "${BROWSER_CACHE}")" || return 1
  out="${out##* path=}"
  [[ -x "${out}" ]] || return 1
  printf '%s\n' "${out}"
}

if ((PREPARE)); then
  browser_path >/dev/null || { echo "test:confine:linux: could not provision the pinned browser" >&2; exit 1; }
  exit 0
fi

# Each go-test-digest.sh call stays on one line: the runner contract reads its scope.
run_leg() {
  local browser
  case "$1" in
    sandbox)
      bash "${ROOT}/scripts/go-test-digest.sh" --full --name test:confine:linux-sandbox -- ./internal/confine/... -count=1
      ;;
    supervisor)
      PW_NATIVE_PRIVILEGE_QUALIFICATION=ci bash "${ROOT}/scripts/go-test-digest.sh" --full --tags nativeprivileges --name test:confine:linux-supervisor -- ./internal/exec ./internal/osprocess -run '^(TestCommandDescendantsFollowsTheWholeTreeOnly|TestSupervisionSettlesTheTree|TestSupervisorExitPreservesThePrimaryStatus|TestParseSupervisorTargetRequiresDescriptorCountAndCommand|TestSupervisorLaunchReportReachesTheParent|TestAwaitEngineLossFollowsTheLifetimePipe|TestStartSupervisorTargetAppliesTheRequestedPriority|TestSignalSupervisorRefusesAReplacedProcess|TestSupervisorCleansDoubleForkedNewSessionOnCommandCompletion|TestSupervisorCancellationSparesAnotherCommandsDetachedDaemon|TestEngineLossFixtureProcess|TestSupervisorEngineSIGKILLRemovesOnlyItsOwnCommands|TestSupervisorEngineLifetimeEOFRemovesDetachedDaemon|TestSupervisorCompanionRequestsOwnedTreeCleanup|TestGuardedPipelinePriorityReachesPrimary|TestSupervisedPTYInteractiveShellOwnsForeground|TestSupervisedPTYInterruptReachesOnlyTheProgram|TestSupervisedCommandSeesOnlyItsIntendedDescriptors|TestTerminationBeforeSupervisorLaunchReportStopsLaunchedCommand|TestHeldTerminationKillsASupervisorThatNeverReportsLaunch|TestParseLinuxStatPreservesKernelFacts|TestParseLinuxStatRejectsMalformedRecords|TestParseLinuxStatBoundsTheEntireRecord|TestLinuxNativePrivilegeQualification|TestOwnedProcessGroupCancellationRetainsNativeSettlement|TestOwnedProcessGroupRefusesConsumedPreparedCommand|TestOwnedProcessGroupWaitsOnceStopsTreeAndPreservesUnrelatedProcess|TestPreparedLaunchFreezesFinalPathEnvironmentAndLiteralArguments|TestPreparedLaunchPreservesRelativeScriptKernelFilename|TestPreparedLaunchRegistersBeforePayloadAndRechecksCurrentAuthority|TestPreparedLaunchRejectsChangedCapturedVectorsBeforeNativeStart|TestPreparedLaunchResolvesRelativeAndEmptyPathAtCapturedCwd|TestAgentPreparedLaunchCurrentActionRefusalNeverReleasesPayload|TestAgentPreparedLaunchPreservesExecuteOnlyNativeBinary|TestAgentPreparedLaunchRequiresCurrentExecutionAuthority|TestPreparedNativeCallbackFailureAndCancellationNeverReleasePayload|TestPreparedLinuxBinaryPreservesNativePathArgvCwdAndExecuteOnlyMode|TestPreparedLinuxScriptPreservesOrdinaryPathAndNativeArgv|TestPreparedInputsSurviveBrokerAndSupervisorPrefixes|TestHelperProbeGroupSharesSupervisedSettlement)$' -count=1
      ;;
    helpers)
      bash "${ROOT}/scripts/go-test-digest.sh" --full --name test:confine:linux-helpers -- ./internal/mcp ./internal/scan/integration ./internal/documentcore ./internal/configlayout -run '^(TestLinuxStdioMCPAppliedBoundary|TestLinuxScannerBoundaryKeepsSourceReadOnlyAndOutputWritable|TestLinuxDocumentCoreAppliedBoundary|TestPrepareStdioCommandConfinedWhenAvailable|TestPrepareStdioCommandHandsTheServerNoCredential|TestPrepareStdioCommandRequiresRootsWhenAvailable|TestRegisterStdioEgressAttributesTheServerLineage|TestBundledScannerConfinementDeniesNetwork|TestNixRetentionReleasesOwnRootAndReclaimsOnlyDeadRoots|TestNixRetentionRefusesUnavailableDaemonAndUnsafeDirectories)$' -count=1
      ;;
    launch)
      bash "${ROOT}/scripts/go-test-digest.sh" --full --name test:confine:linux-launch -- ./internal/launchguard/... ./internal/launchverify/... ./cmd/pw-launch/... ./cmd/pw-launch-entry/... ./cmd/pw-vault-identity/... -count=1
      ;;
    launch-desktop)
      if [[ -z "${PW_LAUNCH_DESKTOP_ARTIFACT:-}" || ! -f "${PW_LAUNCH_DESKTOP_ARTIFACT}" ]]; then
        echo "test:confine:linux: unverified: launch-desktop requires PW_LAUNCH_DESKTOP_ARTIFACT evidence" >&2
        return 2
      fi
      bash "${ROOT}/scripts/go-test-digest.sh" --full --name test:confine:linux-launch-desktop -- ./internal/launchguard/... -tags=protected_desktop_artifact -run '^TestProtectedLaunchActualDesktop$' -count=1
      ;;
    browser)
      if ! browser="$(browser_path)"; then
        echo "test:confine:linux: unverified: the pinned browser could not be provisioned" >&2
        return 2
      fi
      # A browser is provisioned, so a skip would be a green leg covering nothing.
      LYCAON_BROWSER_BIN="${browser}" LYCAON_BROWSER_REQUIRED=1 bash "${ROOT}/scripts/go-test-digest.sh" --full --name test:confine:linux-browser -- ./internal/browser -run '^(TestHeadlessLauncherPinsSubprocessPath|TestHeadlessLauncherKeepsItsSandboxWithoutTheBoundary|TestLaunchHeadlessUnderBrowserConfinement|TestProbeUsabilityUnderBrowserConfinement|TestPoolNewPageUnderBrowserConfinement|TestConfinedBrowserFloorReachesHostLoopback|TestConfinedBrowserCannotReadDeniedFiles|TestBrowserBoundaryRefusesWhenConfinementCannotBeBuilt|TestBrowserBoundaryLaunchesWhenTheHostConfinesNothing|TestBrowserUnavailableLinuxBoundaryDoesNotLaunch|TestLaunchHeadlessRefusesUnsafeCacheDir|TestLaunchHeadlessRefusesUnsafeProjectRootBeforeProvisioning|TestBrowserProcessCancellationSettlesBeforeReturning|TestBrowserProcessUsesOwnedNativeDoorInBothPostures)$' -count=1
      ;;
  esac
}

# A failed leg outranks an unverified one.
STATUS=0
for leg in "${LEGS[@]}"; do
  set +e
  run_leg "${leg}"
  rc=$?
  set -e
  if ((rc == 2)); then
    ((STATUS == 1)) || STATUS=2
  elif ((rc != 0)); then
    STATUS=1
  fi
done
exit "${STATUS}"
