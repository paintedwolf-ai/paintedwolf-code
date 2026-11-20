const TSX = "src/**/*.tsx";
const typedInput = (types) =>
  `JSXOpeningElement[name.name='input']:has(JSXAttribute[name.name='type'][value.value=/^(${types})$/])`;

/** Den draws its own controls; no browser-native chrome is visible. */
export default [
  {
    name: "no-native-select",
    message: "Render DenSelect; a native select paints platform chrome.",
    files: [TSX],
    syntax: ["JSXOpeningElement[name.name=/^(select|option|optgroup)$/]"],
    seed: { file: "src/components/TaskCard.tsx", code: `export const Pick = () => <select><option>one</option></select>;` },
  },
  {
    name: "checkbox-and-radio-are-primitives",
    message: "Render DenCheckbox or DenRadio.",
    files: [TSX],
    ignores: ["src/components/primitives/DenCheckbox.tsx", "src/components/primitives/DenRadio.tsx"],
    syntax: [typedInput("checkbox|radio")],
    seed: { file: "src/components/TaskCard.tsx", code: `export const Tick = () => <input type="checkbox" />;` },
  },
  {
    name: "number-input-is-a-primitive",
    message: "Render DenNumberInput.",
    files: [TSX],
    ignores: ["src/components/primitives/DenNumberInput.tsx"],
    syntax: [typedInput("number")],
    seed: { file: "src/components/TaskCard.tsx", code: `export const Count = () => <input type="number" />;` },
  },
  {
    name: "file-input-stays-hidden",
    message: "A file input is hidden and opened from a Den control.",
    files: [TSX],
    syntax: [`${typedInput("file")}:not(:has(JSXAttribute[name.name='hidden']))`],
    seed: { file: "src/components/TaskCard.tsx", code: `export const Upload = () => <input type="file" />;` },
  },
  {
    name: "disclosure-summaries-carry-a-themed-marker",
    message: "Give the summary a den-disclosure-caret, den-tool-chicklet-caret or den-tool-row-mark; the native marker is suppressed.",
    files: [TSX],
    syntax: [
      "JSXElement[openingElement.name.name='summary']:not(:has(Literal[value=/den-(disclosure-caret|tool-chicklet-caret|tool-row-mark)/]))",
    ],
    seed: { file: "src/components/TaskCard.tsx", code: `export const Fold = () => <details><summary>More</summary></details>;` },
  },
  {
    name: "popups-use-the-anchored-surface",
    message: "Place a menu or listbox with AnchoredSurface.",
    files: [TSX],
    // A structural listbox laid out in flow.
    ignores: ["src/components/transcript/TranscriptVisualFilmstrip.tsx"],
    syntax: [
      "Program:not(:has(JSXIdentifier[name='AnchoredSurface'])) JSXAttribute[name.name='role'][value.value=/^(menu|listbox)$/]",
    ],
    seed: { file: "src/components/TaskCard.tsx", code: `export const Menu = () => <div role="menu" />;` },
  },
  {
    name: "glyphs-go-through-icon-slots",
    message: "Route the glyph through ThemeIcon and give it a slot; an inline svg is one no theme can reach.",
    files: [TSX],
    ignores: [
      "src/components/primitives/ThemeIcon.tsx",
      "src/components/primitives/BrandLockup.tsx",
      // Marks computed from data, not drawn glyphs.
      "src/components/chatview/ContextRing.tsx",
      "src/components/project/ProjectCostComposition.tsx",
      "src/files/walk/StepBar.tsx",
      // Layout-scale teaching arrow.
      "src/components/onboarding/OnboardingGate.tsx",
    ],
    syntax: ["JSXOpeningElement[name.name='svg']"],
    seed: { file: "src/components/TaskCard.tsx", code: `export const Mark = () => <svg viewBox="0 0 16 16" />;` },
  },
];
