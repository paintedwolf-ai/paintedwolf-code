# UI chrome and container mechanics

This document establishes structural, layout, and visual elevation patterns for web application chrome: window title bars, floating command palettes, tab rails, action bars, and modal containers.

## 1. Defining "Chrome" in web interfaces

In web applications, **chrome** comprises the structural framing, affordances, navigation surfaces, and control panels that surround content. Well-crafted chrome remains subordinate to user data while providing unmistakable cues for state, hierarchy, and affordance.

---

## 2. Chrome component patterns

### Desktop window header (Traffic lights and tab strip)

Desktop and developer tool windows require clean headers with window management controls, document tabs, and quick actions:

- **Window management dots (macOS style)**:
  - Size: `12×12px` circles.
  - Gap: `8px` center-to-center spacing.
  - Colors: Close (`#ff5f56`), Minimize (`#ffbd2e`), Maximize (`#27c93f`), with subtle `0.5px` border to define edges against bright backgrounds.
- **Tab bars**:
  - Inactive tabs: Transparent background, muted text (`var(--kit-muted)`), subtle hover feedback.
  - Active tab: Surface background (`var(--kit-surface)`), top accent indicator or 1px border highlight, contrast text (`var(--kit-fg)`).
  - Close button: `14×14px` touch target with `stroke-width: 1.5px`, transitioning on hover.

```html
<header style="display:flex; align-items:center; height:38px; padding:0 var(--kit-space-3); background:var(--kit-bg); border-bottom:1px solid var(--kit-border); font-family:'Inter'">
  <!-- Window controls -->
  <div style="display:flex; gap:8px; margin-right:var(--kit-space-4)">
    <span style="width:12px; height:12px; border-radius:50%; background:#ff5f56; display:inline-block"></span>
    <span style="width:12px; height:12px; border-radius:50%; background:#ffbd2e; display:inline-block"></span>
    <span style="width:12px; height:12px; border-radius:50%; background:#27c93f; display:inline-block"></span>
  </div>
  <!-- Tab rail -->
  <div style="display:flex; gap:2px; height:100%">
    <div style="display:flex; align-items:center; gap:6px; padding:0 12px; background:var(--kit-surface); border:1px solid var(--kit-border); border-bottom:none; border-radius:var(--kit-radius) var(--kit-radius) 0 0; color:var(--kit-fg); font-size:var(--kit-text-xs); font-weight:500">
      <svg class="kit-icon" width="14" height="14" style="color:var(--kit-accent)"><use href="#kit-file-code"/></svg>
      <span>editor.ts</span>
    </div>
  </div>
</header>
```

---

### Floating command palette (Cmd+K)

Modern application command palettes demand high-focus modal chrome with frosted glass surfaces and keyboard navigation affordances:

- **Surface styling**:
  - Max width: `560px` to `640px`.
  - Elevation: High-depth dual drop shadow.
  - Frosted glass: `backdrop-filter: blur(16px)` with semi-transparent surface background (`rgba(..., 0.85)`).
- **Search input**:
  - Integrated search icon (`20×20px`, outline or duotone).
  - Borderless, transparent input container with typography set to `var(--kit-text-base)`.
- **Keyboard shortcut badges (`<kbd>`)**:
  - Border: `1px solid var(--kit-border)`.
  - Font: Mono stack (`var(--kit-font-mono)` or `JetBrains Mono`).
  - Size: `10px` or `11px`, with vertical centering.

```html
<div class="kit-surface" style="width:580px; border-radius:12px; box-shadow:0 20px 40px -15px rgba(0,0,0,0.3); border:1px solid var(--kit-border); overflow:hidden; font-family:'Inter'">
  <!-- Search header -->
  <div style="display:flex; align-items:center; gap:12px; padding:14px 16px; border-bottom:1px solid var(--kit-border)">
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="color:var(--kit-muted)">
      <circle cx="11" cy="11" r="8"/><path d="m21 21-4.35-4.35"/>
    </svg>
    <span style="color:var(--kit-muted); font-size:var(--kit-text-base); flex-grow:1">Type a command or search files...</span>
    <kbd style="background:var(--kit-bg); color:var(--kit-muted); border:1px solid var(--kit-border); border-radius:4px; padding:2px 6px; font-size:11px; font-family:'JetBrains Mono'">ESC</kbd>
  </div>
  <!-- Action item list -->
  <div style="padding:6px">
    <div style="display:flex; align-items:center; justify-content:space-between; padding:8px 10px; border-radius:6px; background:var(--kit-accent); color:var(--kit-accent-fg); font-size:var(--kit-text-sm)">
      <div style="display:flex; align-items:center; gap:8px">
        <svg class="kit-icon" width="16" height="16"><use href="#kit-sparkles"/></svg>
        <span>Generate component</span>
      </div>
      <kbd style="border:1px solid rgba(255,255,255,0.25); border-radius:4px; padding:1px 5px; font-size:10px; font-family:'JetBrains Mono'">↵</kbd>
    </div>
  </div>
</div>
```

---

### Segmented control & pill tab switcher

Mode toggles and view switchers (e.g. Board | List | Timeline) require high-contrast active pill elevation:

- **Enclosing track**: `background: var(--kit-bg); border: 1px solid var(--kit-border); border-radius: 9999px; padding: 3px; gap: 2px;` with subtle inset shadow.
- **Active segment**: Surface fill (`var(--kit-surface)`), contrast text (`var(--kit-fg)`), `border-radius: 9999px`, and drop shadow `0 1px 3px rgba(0,0,0,0.12), inset 0 1px 0 rgba(255,255,255,0.08)`.
- **Inactive segments**: Transparent fill, muted text (`var(--kit-muted)`), transitioning to `var(--kit-fg)` on hover.
- **Embedded micro-badges**: 10px bold numeric count pill inside active segment.
- Specimen: [`assets/segmented-control.html`](../assets/segmented-control.html).

---

### Floating contextual action dock

For bulk actions on selected items (e.g. "3 tasks selected · Complete · Move · Tag · Delete"):

- **Surface**: Floating pill container with `backdrop-filter: blur(16px); background: var(--kit-surface); border: 1px solid var(--kit-border); border-radius: 12px`.
- **Elevation**: Multi-stop floating shadow: `box-shadow: 0 12px 32px -8px rgba(0,0,0,0.25), inset 0 1px 0 rgba(255,255,255,0.08)`.
- **Dividers**: 18px tall vertical 1px rules with `background: var(--kit-border)`.
- **Actions**: Paired with 14×14px outline icons and inline monospace keyboard shortcut badges (`<kbd>`).
- Specimen: [`assets/floating-dock.html`](../assets/floating-dock.html).

---

### Kanban column & draggable task card chrome

Productivity and SaaS board interfaces require clean column framing and tactile card physics:

- **Column container**: Tinted track (`var(--kit-bg)`), 1px border, 12px radius, and header with status dot, title, count pill, and quick `+` add action.
- **Card surface**: Elevated card (`var(--kit-surface)`), 1px border, 8px radius, contact shadow (`0 1px 3px rgba(0,0,0,0.08)`), and top specular highlight (`inset 0 1px 0 rgba(255,255,255,0.06)`).
- **Affordances**:
  - Drag handle: 6-dot matrix icon (`⠿`).
  - Priority badge: Semitransparent tinted pill (e.g. `rgba(239,68,68,0.12)` for High priority) with matching 10×10 icon.
  - Footer meta: Due date clock icon and circular assignee initials avatar with contrast accent background.
- Specimen: [`assets/kanban-card.html`](../assets/kanban-card.html).

---

### KPI stat & metric summary widget

Dashboard metric widgets convey high-density data through structured hierarchy:

- **Surface**: Elevated card with subtle corner action menu (`⋮`).
- **Tabular figures**: `font-variant-numeric: tabular-nums; font-size: 28px; font-weight: 700; letter-spacing: -0.02em`.
- **Trend pill badge**: Pill badge with inline direction arrow (e.g. green `+14.2%` with upward polyline), 1px border, and rounded pill shape.
- **Footer baseline**: 1px top border separator and muted comparison text with 12×12 clock or calendar icon.
- Specimen: [`assets/kpi-metric-card.html`](../assets/kpi-metric-card.html).

---

### Toast notification & status banner

Ephemeral feedback and system announcements:

- **Container**: Elevated floating banner (`var(--kit-surface)`), 1px border, and a bold **left accent indicator border** (`border-left: 3px solid var(--kit-accent)`).
- **Elevation**: `box-shadow: 0 10px 25px -5px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.06)`.
- **Components**: Leading status icon, title, description, inline text action button ("View" or "Undo"), and trailing dismiss `✕` button.
- Specimen: [`assets/toast-notification.html`](../assets/toast-notification.html).

---

## 3. Surface elevation physics

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

## 4. Permissively licensed design system patterns

Modern web application chrome draws from proven architectural patterns in permissively licensed open-source UI libraries:

| System | License | Core architectural inspiration |
|--------|---------|--------------------------------|
| **shadcn/ui** | **MIT** | Zero-runtime composable markup, semantic CSS variable tokens (`--background`, `--foreground`, `--card`, `--border`, `--ring`), copy-paste component ownership. |
| **Radix UI Primitives** | **MIT** | Unstyled accessible state primitives: `role="tablist"`, `aria-selected`, `role="status"`, focus-visible rings, and modal focus trapping. |
| **Shoelace / Web Components** | **MIT** | Shadow DOM token encapsulation, standardized badge and pill geometries, clear size tiers (`small`, `medium`, `large`). |
| **Tailwind UI Patterns** | **MIT (open components)** | Multi-layer drop shadows (`shadow-sm`, `shadow-md`, `shadow-xl`), subtle ring borders (`ring-1 ring-inset`), and tabular numeric alignment. |

All specimen assets in `assets/` synthesize these architectural patterns into standalone, zero-dependency HTML components styled with the host design kit tokens.

