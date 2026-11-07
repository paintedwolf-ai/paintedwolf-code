import { getAllByRole, type ByRoleMatcher } from "@testing-library/dom";

/**
 * Asserts that a control exposes the role and a non-empty accessible name.
 *
 * Tests find controls by test id, so rewording a label cannot break them. This
 * keeps what a name query also proved: assistive technology can reach the
 * control by name. It searches only the control's parent, not the whole page.
 */
export function assertNamedControl<E extends HTMLElement>(control: E, role: ByRoleMatcher): E {
  const scope = control.parentElement ?? control.ownerDocument.body;
  const named = getAllByRole(scope, role, { name: /\S/, hidden: true });
  if (!named.includes(control)) {
    throw new Error(`control is not a named ${String(role)}: ${control.outerHTML.slice(0, 200)}`);
  }
  return control;
}
