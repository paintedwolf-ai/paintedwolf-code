export type ConfirmDestructiveRequest = {
  message: string;
  title: string;
  okLabel: string;
  cancelLabel?: string;
  destructive?: boolean;
};

export type ConfirmDestructivePresenter = (
  req: ConfirmDestructiveRequest,
) => Promise<boolean>;

let presenter: ConfirmDestructivePresenter | null = null;

export function setConfirmDestructivePresenter(
  next: ConfirmDestructivePresenter | null,
): void {
  presenter = next;
}

export async function confirmDestructive(
  req: ConfirmDestructiveRequest,
): Promise<boolean> {
  if (presenter) return presenter(req);
  return window.confirm(req.message);
}
