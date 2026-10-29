# Chrome markup snippets

Inline starting points on `--kit-*` tokens. Full specimens live in `assets/`.

## Desktop window header with traffic lights and tab strip

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

## Floating command palette

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
