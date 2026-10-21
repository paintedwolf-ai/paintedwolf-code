/** `keyCode` some input methods report on the key that commits a candidate. */
export const IME_COMPOSITION_KEY_CODE = 229;

/**
 * Whether a key event belongs to an input method composition. WebKitGTK and
 * some input methods deliver the commit key with the legacy composition key
 * code instead of `isComposing`, so both signals count. Local Enter, Escape,
 * Tab, navigation and command handling leave these keys to the input method.
 */
export function isImeComposition(event: { isComposing?: boolean; keyCode?: number }): boolean {
  return event.isComposing === true || event.keyCode === IME_COMPOSITION_KEY_CODE;
}
