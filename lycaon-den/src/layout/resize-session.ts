/** Reversible preview with one terminal outcome. */
export type ResizeSession = {
  preview: (value: number) => void;
  commit: () => void;
  cancel: () => void;
};
