import { SECURITY_SCANNERS_SECTION_LABEL } from "../settings-nav-model.ts";

export const SCANNERS_SETTINGS_COPY = {
  title: SECURITY_SCANNERS_SECTION_LABEL,
  threatBanner:
    "External scanners run as local programs with your account. Only you install them — extension packs cannot.",
  productIntro:
    "One scanner per job — static analysis, dependencies, and secrets. The engines we bundle are selected by default and named below; swap in a tool you already use.",
  mainLabel: `Use ${SECURITY_SCANNERS_SECTION_LABEL}`,
  mainHint: `Off hides ${SECURITY_SCANNERS_SECTION_LABEL} from project Context and stops all scanning.`,

  // Scanner slots.
  jobsHeading: "Jobs",
  slotLabel: {
    sast: "Static analysis",
    sca: "Dependencies",
    secret: "Secrets",
  } as Record<string, string>,
  slotHint: {
    sast: "Finds insecure patterns in your code.",
    sca: "Finds known CVEs in your dependencies.",
    secret: "Finds credentials committed to the repo.",
  } as Record<string, string>,
  slotEmpty: "No scanner selected.",
  changeButton: "Change",

  // Slot picker.
  chooseFor: (job: string) => `Choose a ${job.toLowerCase()} scanner`,
  slotPickerIntro:
    "One tool handles this job. Choosing another replaces the current one.",
  recommendedBadge: "Default",
  currentBadge: "Current",
  installFirst: "Install to use",
  needsBinary: "Not installed",
  rejected: "Rejected",
  deleteButton: "Remove",
  addCancel: "Close",

  // Custom scanners.
  customGroup: "Custom",
  customOption: "Custom scanner…",
  customTitle: "Add a custom scanner",
  customIntro:
    "For a CLI we don't know yet. It must print a SARIF report to stdout.",
  customSubmit: "Add",
  fieldId: "Id",
  fieldIdHint: "Lowercase name used in scan results, for example acme-sast.",
  fieldCommand: "Command",
  fieldCommandHint:
    "The exact command to run, space separated. Use {{project_dir}} for the project path. No secrets, no shell syntax.",
  fieldLabel: "Display name",
  fieldSoftLimit: "Long-running notice (seconds)",
  fieldSoftLimitHint: "The scan keeps running after this point; the app reports that it is taking longer than usual.",
  fieldHardLimit: "Maximum runtime (seconds)",
  fieldHardLimitHint: "Zero lets the scan finish without a deadline. Set a positive value to stop it after that many seconds.",
  fieldCPUUnits: "CPU weight",
  fieldParallelism: "Scanner workers",
  runtimeInvalid: "Use whole positive numbers, or zero for no maximum runtime. A positive maximum must meet or exceed the notice. CPU weight must be at most 8, and scanner workers at most 16.",
  runtimeSummary: (soft: number, hard: number, cpu: number, workers: number) =>
    `${Math.round(soft / 60)}m notice · ${hard === 0 ? "No deadline" : `${Math.round(hard / 60)}m max`} · CPU ${cpu} · ${workers} worker${workers === 1 ? "" : "s"}`,

  // Install checks.
  checkInstalls: "Check installs",
  checkingInstalls: "Checking…",
  installOK: "Works",
  installFailed: "Failed",
  checkInstallsError: "Could not check scanner installs.",

  pathHint: "Device catalog",
  pathHintFallback: "~/.config/…/scanners.yaml",
  saving: "Saving…",
  loadError: "Could not load scanner settings.",
  saveError: "Could not save scanner settings.",
  createError:
    "Could not add scanner. Check the id and command, then try again.",
  deleteError: "Could not remove scanner.",

  landedChangeLabel: "After changes land",
  landedChangeHint:
    "Changed paths scans only files that land from a worker. Whole project scans the full tree after each landed change.",
  landedChangePathScoped: "Changed paths",
  landedChangeFullRoot: "Whole project",

  sourceVerifyLabel: "Reading the source",
  sourceVerifyHint:
    "By timestamp reuses a file's last reading while its size, permissions, and modified time are unchanged, so a scan reads only what moved. By content reads every file each time — slower on a large project, and the only way to catch a file rewritten to the same size within one timestamp.",
  sourceVerifyStat: "By timestamp",
  sourceVerifyContent: "By content",
} as const;

export function scannerSlotLabel(slot: string): string {
  return SCANNERS_SETTINGS_COPY.slotLabel[slot] ?? slot;
}
