import { Show, createSignal } from "solid-js";
import { nativePathDialog } from "../../../platform/files/native-path-dialog.ts";
import { isTauriRuntime } from "../../../platform/runtime.ts";
import { DenButton } from "../../primitives/DenButton.tsx";

type Props = {
  busy: boolean;
  onImport: (path: string) => Promise<void>;
  onFailure: (message: string) => void;
};

export function OfflineModelImport(props: Props) {
  const [path, setPath] = createSignal("");
  const pick = async (folder: boolean) => {
    try {
      const picked = await nativePathDialog("Import offline model bundle", folder
        ? { kind: "folder" } : { kind: "file", filter: { name: "Model bundles", extensions: ["pwmodel", "tar"] } });
      if (picked) await props.onImport(picked.path);
    } catch (err) {
      props.onFailure(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <details class="den-settings-hint">
      <summary><span class="den-disclosure-caret" aria-hidden="true" />Import offline bundle</summary>
      <div class="den-settings-provider-actions">
        <Show when={isTauriRuntime()} fallback={
          <>
            <label>Bundle file or folder
              <input value={path()} onInput={event => setPath(event.currentTarget.value)} />
            </label>
            <DenButton variant="secondary" disabled={props.busy || !path().trim()} onClick={() => void props.onImport(path().trim())}>Import bundle</DenButton>
          </>
        }>
          <DenButton variant="secondary" disabled={props.busy} onClick={() => void pick(false)}>Choose archive…</DenButton>
          <DenButton variant="secondary" disabled={props.busy} onClick={() => void pick(true)}>Choose folder…</DenButton>
        </Show>
      </div>
    </details>
  );
}
