import { createStore, produce } from "solid-js/store";
import type { SecretScreen } from "../../api/types.ts";

type ScreenState = Record<string, SecretScreen | undefined>;

const [screens, setScreens] = createStore<ScreenState>({});

export function setFilesEditorSecretScreen(
  bufferKey: string,
  screen: SecretScreen | null | undefined,
): void {
  setScreens(
    produce((state) => {
      if (screen) state[bufferKey] = screen;
      else delete state[bufferKey];
    }),
  );
}

export function clearFilesEditorSecretScreen(bufferKey: string): void {
  setFilesEditorSecretScreen(bufferKey, null);
}

export function filesEditorSecretScreen(bufferKey: string): SecretScreen | undefined {
  return screens[bufferKey];
}
