/** Ancestors that take an element out of sight and out of reach. */
const UNPRESENTED_SELECTOR = '[hidden],[inert],[aria-hidden="true"]';

/** Whether the user can see and reach this element. */
export function isPresented(element: Element): boolean {
  return element.closest(UNPRESENTED_SELECTOR) == null;
}
