import { createPresentationIntent } from "../../ui/presentation.ts";

const navigationIntent = createPresentationIntent();

/** Reference lookups and direct navigation share the window's latest intent. */
export function beginSourceNavigation() {
  return navigationIntent.begin();
}
