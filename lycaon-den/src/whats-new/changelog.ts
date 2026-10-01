import changelogRaw from "../../../CHANGELOG.md?raw";

/** Build-time root CHANGELOG.md (same file the release manifest and website use). */
export const CHANGELOG_RAW: string = changelogRaw;

/**
 * Return the Keep-a-Changelog body for `version`, or "" when absent.
 *
 * Headings are date-suffixed (`## [0.1.0] — 2026-07-15`); matching is a
 * prefix on `## [<version>]`. A leading `v` on the query is ignored. Dev
 * versions and `Unreleased` simply miss.
 */
export function sectionFor(changelog: string, version: string): string {
  const needle = normalizeChangelogVersion(version);
  if (!needle) return "";

  const lines = changelog.split(/\r?\n/);
  let bodyStart = -1;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    if (!line.startsWith("## [")) continue;
    const after = line.slice("## [".length);
    if (after.startsWith(`${needle}]`)) {
      bodyStart = i + 1;
      break;
    }
  }
  if (bodyStart < 0) return "";

  let bodyEnd = lines.length;
  for (let i = bodyStart; i < lines.length; i++) {
    if (lines[i]!.startsWith("## [")) {
      bodyEnd = i;
      break;
    }
  }
  return lines.slice(bodyStart, bodyEnd).join("\n").trim();
}

/** Notes for the running version from the build-time changelog. */
export function notesForVersion(version: string): string {
  return sectionFor(CHANGELOG_RAW, version);
}

function normalizeChangelogVersion(version: string): string {
  return version.trim().replace(/^v/i, "");
}
