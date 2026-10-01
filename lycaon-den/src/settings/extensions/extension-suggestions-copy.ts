export const EXTENSION_SUGGESTIONS_COPY = {
  dialogTitle: (name: string) => `Suggested extensions for ${name}`,
  openAction: "Open project",
  cloneAction: "Clone & open",
  cancel: "Cancel",
  title: (n: number) =>
    `This project suggests ${n} extension${n === 1 ? "" : "s"}`,
  scope: "Installs on this device",
  selectAll: "Select all",
  selectNone: "Select none",
  selectedCount: (n: number) => `${n} selected`,
  showAll: (n: number) => `Show all ${n}`,
  foot: "Installed once, available to every project. Nothing is added to this folder.",
  skipNote: "Skip and they stay listed under Extensions.",
  installAndOpen: (n: number, verb: "open" | "clone") => `Install ${n} & ${verb}`,
} as const;
