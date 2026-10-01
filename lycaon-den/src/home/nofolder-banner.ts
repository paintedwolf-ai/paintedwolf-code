/** Shows the folder prompt for a ready, rootless saved project. */
export type NoFolderBannerInput = {
  isDraft: boolean;
  rootCount: number;
  dismissed: boolean;
  chatReady: boolean;
};

export function shouldShowNoFolderBanner(input: NoFolderBannerInput): boolean {
  if (!input.chatReady) return false;
  if (input.isDraft) return false;
  if (input.rootCount !== 0) return false;
  if (input.dismissed) return false;
  return true;
}
