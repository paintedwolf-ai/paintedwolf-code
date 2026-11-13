/** Shows the folder prompt for a ready saved project with no folder agents use. */
export type NoFolderBannerInput = {
  isDraft: boolean;
  agentRootCount: number;
  dismissed: boolean;
  chatReady: boolean;
};

export function shouldShowNoFolderBanner(input: NoFolderBannerInput): boolean {
  if (!input.chatReady) return false;
  if (input.isDraft) return false;
  if (input.agentRootCount !== 0) return false;
  if (input.dismissed) return false;
  return true;
}
