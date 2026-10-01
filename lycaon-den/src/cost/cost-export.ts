function yyyymmdd(d = new Date()): string {
  const y = d.getUTCFullYear();
  const m = String(d.getUTCMonth() + 1).padStart(2, "0");
  const day = String(d.getUTCDate()).padStart(2, "0");
  return `${y}${m}${day}`;
}

/** Download name for a project cost export — "my-app-cost-20260807.csv". */
export function costExportFilename(
  projectName: string | undefined,
  ext: "csv" | "json",
): string {
  const slug = (projectName?.trim() || "project")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "") || "project";
  return `${slug}-cost-${yyyymmdd()}.${ext}`;
}
