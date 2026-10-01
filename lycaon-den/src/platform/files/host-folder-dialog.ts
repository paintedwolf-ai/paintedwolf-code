export type HostFolderPresenter = () => Promise<string | null>;

let presenter: HostFolderPresenter | null = null;

export function setHostFolderPresenter(next: HostFolderPresenter | null): void {
  presenter = next;
}

/** Host paths are entered in the app; browser prompts may be unavailable. */
export function requestHostFolder(): Promise<string | null> {
  if (!presenter) return Promise.reject(new Error("The folder picker is not ready. Try again."));
  return presenter();
}
