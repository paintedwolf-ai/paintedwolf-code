# Chrome elevation and tokens

## Surface elevation

Achieving tactile, premium UI chrome requires layering multiple optical cues rather than relying on a single flat border:

1. **Inset highlight line**:
   A delicate 1px translucent highlight at the top edge simulates overhead environmental lighting:
   ```css
   box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.12), 0 8px 24px -4px rgba(0, 0, 0, 0.2);
   ```
2. **Double border containment**:
   In dark mode, combine an outer dark ambient glow with a high-contrast 1px border to keep surfaces visually crisp against deep backdrops.
3. **Keyboard shortcut badges (`<kbd>`)**:
   Standardize keyboard shortcuts across modals, toolbars, and menus:
   ```css
   kbd {
     background: var(--kit-bg);
     color: var(--kit-muted);
     border: 1px solid var(--kit-border);
     border-radius: 4px;
     padding: 1px 5px;
     font-size: 10px;
     font-family: 'JetBrains Mono', monospace;
   }
   ```
4. **Design kit token integration**:
   Always build chrome using the host design kit tokens so that components transition between dark and light themes effortlessly:
   - Surface background: `var(--kit-surface)`
   - Outer frame border: `var(--kit-border)`
   - Window canvas background: `var(--kit-bg)`
   - Primary copy & labels: `var(--kit-fg)`
   - Secondary metadata & shortcuts: `var(--kit-muted)`
   - Active selections & highlights: `var(--kit-accent)` and `var(--kit-accent-fg)`

---

## Permissively licensed design system patterns

Modern web application chrome draws from proven architectural patterns in permissively licensed open-source UI libraries:

| System | License | Core architectural inspiration |
|--------|---------|--------------------------------|
| **shadcn/ui** | **MIT** | Zero-runtime composable markup, semantic CSS variable tokens (`--background`, `--foreground`, `--card`, `--border`, `--ring`), copy-paste component ownership. |
| **Radix UI Primitives** | **MIT** | Unstyled accessible state primitives: `role="tablist"`, `aria-selected`, `role="status"`, focus-visible rings, and modal focus trapping. |
| **Shoelace / Web Components** | **MIT** | Shadow DOM token encapsulation, standardized badge and pill geometries, clear size tiers (`small`, `medium`, `large`). |
| **Tailwind UI Patterns** | **MIT (open components)** | Multi-layer drop shadows (`shadow-sm`, `shadow-md`, `shadow-xl`), subtle ring borders (`ring-1 ring-inset`), and tabular numeric alignment. |

All specimen assets in `assets/` synthesize these architectural patterns into standalone, zero-dependency HTML components styled with the host design kit tokens.

