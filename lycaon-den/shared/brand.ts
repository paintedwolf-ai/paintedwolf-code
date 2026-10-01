/** User-visible product name. */
export const PRODUCT_NAME = "Painted Wolf Code" as const;

/** macOS bundle id. */
export const BUNDLE_IDENTIFIER = "dev.paintedwolf.code" as const;

/** Public website. */
export const WEBSITE_URL = "https://paintedwolf.ai";

/** Public source repository. */
export const REPOSITORY_URL = "https://github.com/paintedwolf-ai/paintedwolf-code";

/** Public issue tracker URL. */
export const ISSUES_URL = `${REPOSITORY_URL}/issues`;

/** Website page for one release; the site builds one per CHANGELOG.md section. */
export function releasePageUrl(version: string): string {
  return `${WEBSITE_URL}/releases/${version.trim().replace(/^v/i, "")}/`;
}

/** Release config directory name. */
export const CONFIG_DIR_NAME = "paintedwolf" as const;

/** Development config directory name. */
export const CONFIG_DIR_NAME_DEV = "paintedwolf-dev" as const;

/** Human label for the release config dir. */
export const CONFIG_DIR_LABEL = `~/.config/${CONFIG_DIR_NAME}` as const;

/** Human label for the development config dir. */
export const CONFIG_DIR_LABEL_DEV = `~/.config/${CONFIG_DIR_NAME_DEV}` as const;

/** Desktop application executable name. */
export const MAIN_BINARY_NAME = "painted-wolf-code" as const;
