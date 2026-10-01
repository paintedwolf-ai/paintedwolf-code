/** Every arrival flash's duration, in CSS and editor themes alike. */
export const REVEAL_FLASH_MS = 2200;

/** Styled in styling/shell/reveal-flash-domain.css. */
export const REVEAL_FLASH_CLASS = "den-reveal-flash";

const clearTimers = new WeakMap<HTMLElement, number>();

/** Flash a revealed element, restarting the flash when it is already running. */
export function flashRevealTarget(element: HTMLElement): void {
  const pending = clearTimers.get(element);
  if (pending !== undefined) clearTimeout(pending);
  element.classList.remove(REVEAL_FLASH_CLASS);
  // A style read between remove and add restarts the animation.
  void element.offsetWidth;
  element.classList.add(REVEAL_FLASH_CLASS);
  clearTimers.set(
    element,
    window.setTimeout(() => {
      clearTimers.delete(element);
      element.classList.remove(REVEAL_FLASH_CLASS);
    }, REVEAL_FLASH_MS),
  );
}
