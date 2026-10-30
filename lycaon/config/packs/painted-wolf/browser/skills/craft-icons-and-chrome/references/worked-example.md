# Worked example: command palette search row

A duotone search icon on the 24-unit grid inside elevated chrome, using only `--kit-*` tokens.

```html
<div class="kit-surface" style="display:flex; align-items:center; gap:var(--kit-space-2); padding:6px 12px; border-radius:var(--kit-radius); max-width:320px; font-family:'Inter'">
  <svg width="20" height="20" viewBox="0 0 24 24" fill="none" aria-hidden="true" style="color:var(--kit-muted); flex-shrink:0">
    <circle cx="11" cy="11" r="8" fill="currentColor" fill-opacity="0.2"/>
    <circle cx="11" cy="11" r="8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
    <path d="m21 21-4.35-4.35" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
  </svg>
  <span style="color:var(--kit-muted); font-size:var(--kit-text-sm); flex-grow:1">Search tools...</span>
  <kbd style="background:var(--kit-bg); color:var(--kit-muted); border:1px solid var(--kit-border); border-radius:4px; padding:2px 6px; font-size:var(--kit-text-xs); font-family:'JetBrains Mono'">⌘K</kbd>
</div>
```
