# Worked example: a deployment status card

Realistic domain data on kit tokens, a status badge, and a kit icon button.

```html
<div class="kit-surface" style="padding:var(--kit-space-4); max-width:480px; font-family:'Inter'">
  <header style="display:flex; justify-content:space-between; align-items:center; margin-bottom:var(--kit-space-3)">
    <h3 style="margin:0; font-size:var(--kit-text-lg); color:var(--kit-fg)">Active deployment</h3>
    <span style="background:var(--kit-accent); color:var(--kit-accent-fg); padding:2px 8px; border-radius:var(--kit-radius); font-size:var(--kit-text-xs)">Live</span>
  </header>
  <p style="color:var(--kit-muted); font-size:var(--kit-text-sm); margin:0 0 var(--kit-space-3) 0">Cluster us-east-2 · rev 4a9f12c</p>
  <div style="display:flex; gap:var(--kit-space-2)">
    <button style="display:inline-flex; align-items:center; gap:6px; background:var(--kit-bg); color:var(--kit-fg); border:1px solid var(--kit-border); padding:6px 12px; border-radius:var(--kit-radius); cursor:pointer">
      <svg class="kit-icon" aria-hidden="true"><use href="#kit-settings"/></svg> Configure
    </button>
  </div>
</div>
```
