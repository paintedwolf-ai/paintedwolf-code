import type { Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { documentStyleNonce } from "../../../platform/csp-nonce.ts";

export function editorCspNonce(): Extension {
  const nonce = documentStyleNonce();
  return nonce ? EditorView.cspNonce.of(nonce) : [];
}
