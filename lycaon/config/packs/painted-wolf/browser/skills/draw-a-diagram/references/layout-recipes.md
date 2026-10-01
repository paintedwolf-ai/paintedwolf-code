# Layout recipes

Adaptable skeletons for `render_view` diagrams. Each stays entirely on kit tokens so it renders correctly in both themes and under a project brand overlay. Swap labels, counts, and coordinates; keep the structure.

## Pipeline flow (SVG)

Left-to-right stages with arrows. Compute x positions from a fixed stage width plus gap; arrows run edge to edge.

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1240 220" font-family="Inter" font-size="15">
  <defs>
    <marker id="arr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto">
      <path d="M0 0L10 5L0 10z" fill="var(--kit-muted)"/>
    </marker>
  </defs>
  <!-- stage boxes: x = 40 + i*300, width 220 -->
  <g>
    <rect x="40" y="70" width="220" height="80" rx="12" fill="var(--kit-surface)" stroke="var(--kit-border)"/>
    <text x="150" y="115" text-anchor="middle" fill="var(--kit-fg)">Ingest</text>
  </g>
  <line x1="260" y1="110" x2="332" y2="110" stroke="var(--kit-muted)" stroke-width="2" marker-end="url(#arr)"/>
  <!-- repeat for each stage; annotate an edge with a small label above it -->
  <text x="296" y="96" text-anchor="middle" font-size="12" fill="var(--kit-muted)">queue</text>
</svg>
```

## Layered architecture (SVG)

Full-width horizontal bands, one per layer, stacked top to bottom; components as small boxes inside a band.

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1240 460" font-family="Inter" font-size="15">
  <!-- band: y = 30 + layer*140 -->
  <rect x="30" y="30" width="1180" height="120" rx="12" fill="var(--kit-surface)" stroke="var(--kit-border)"/>
  <text x="52" y="60" fill="var(--kit-muted)" font-size="12">UI layer</text>
  <!-- components inside the band: x = 52 + i*230 -->
  <rect x="52" y="76" width="200" height="52" rx="8" fill="var(--kit-bg)" stroke="var(--kit-border)"/>
  <text x="152" y="107" text-anchor="middle" fill="var(--kit-fg)">Transcript</text>
</svg>
```

## Timeline (HTML)

A two-column grid — markers in a thin left rail, events at the right. Flow layout handles uneven text.

```html
<div style="display:grid;grid-template-columns:16px 1fr;gap:0 16px;max-width:720px;
            padding:var(--kit-space-6);font-family:'Inter'">
  <div style="display:flex;flex-direction:column;align-items:center">
    <span style="width:12px;height:12px;border-radius:999px;background:var(--kit-accent)"></span>
    <span style="flex:1;width:2px;background:var(--kit-border)"></span>
  </div>
  <div style="padding-bottom:var(--kit-space-5)">
    <div style="font-size:var(--kit-text-sm);color:var(--kit-muted)">March</div>
    <div style="font-weight:600">First public tag</div>
    <div style="color:var(--kit-muted)">One sentence of detail.</div>
  </div>
  <!-- repeat marker + event rows -->
</div>
```

## Comparison cards (HTML)

Side-by-side options as equal cards; a highlighted border marks a recommendation.

```html
<div style="display:grid;grid-template-columns:repeat(3,1fr);gap:var(--kit-space-4);
            padding:var(--kit-space-6);font-family:'Inter'">
  <div class="kit-surface" style="padding:var(--kit-space-5)">
    <div style="font-size:var(--kit-text-lg);font-weight:600">Option A</div>
    <ul style="margin:var(--kit-space-3) 0 0;padding-left:1.1em;color:var(--kit-muted)">
      <li>Trade-off one</li>
      <li>Trade-off two</li>
    </ul>
  </div>
  <div class="kit-surface" style="padding:var(--kit-space-5);border-color:var(--kit-accent)">
    <!-- recommended option -->
  </div>
</div>
```
