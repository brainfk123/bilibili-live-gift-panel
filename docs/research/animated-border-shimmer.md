# Rounded-border shimmer techniques

## Problem

The current tutorial highlight moves a straight, glowing capsule along a rounded-rectangle motion path. CSS `offset-distance` keeps the capsule's anchor moving at a uniform distance along the path, but `offset-rotate` only rotates the rigid capsule to the path tangent. It does not bend the capsule around a corner. A long capsule therefore cuts across or protrudes from a small-radius corner.

## Common approaches

| Technique | How it works | Corner fidelity | Speed characteristics | Best use |
| --- | --- | --- | --- | --- |
| Rotating `conic-gradient()` plus a border mask | Rotate angular color stops behind a border-shaped mask | The visible paint is clipped to the border, so it does not protrude | Uniform angular speed is not uniform perimeter distance on a long rectangle | Decorative full-border color rotation where exact travel speed is unimportant |
| CSS motion path | Move a small element with `offset-path` and animate `offset-distance` | Its anchor follows the rounded path, but a long straight element does not bend | `offset-distance` percentages refer to total path length and animate cleanly | Dots, sparks, or very short highlights |
| SVG rounded-rectangle stroke | Draw the border as an SVG `<rect>`, make one dash visible with `stroke-dasharray`, and animate `stroke-dashoffset` | The bright dash is part of the rounded path itself and bends through corners | Dash positions and offsets are measured along the path | Long glowing dashes that must remain attached to rounded corners |

## Recommendation

Use an absolutely positioned SVG overlay containing two matching rounded rectangles:

1. A faint static stroke for the complete outline.
2. Several synchronized dashed strokes with `stroke-linecap: round`: a long translucent tail, a shorter accent-colored body, and a short bright head. Their overlapping lengths create a directional gradient that still follows the curved path.
3. Normalize the path with `pathLength="100"`, then animate the bright stroke's numeric `stroke-dashoffset` from `0` to `-100` with a linear timing function.
4. Apply a small glow filter to the tail and head, while keeping the stroke widths close to the actual border thickness. A single solid stroke plus `drop-shadow` only softens its outside edge; it does not create a gradient along the direction of travel.
5. Disable only the moving stroke under `prefers-reduced-motion`, leaving the static outline visible.

This produces both properties needed here: constant travel along the perimeter and a highlight that genuinely curves around the same rounded rectangle. Shortening the current capsule or reducing its shadow can hide the defect, but cannot make a rigid line conform to an arc.

## Primary sources

- [CSS `offset-path`](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/offset-path) — a coordinate-box path follows the containing block's rounded border shape.
- [CSS `offset-distance`](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/offset-distance) — `100%` represents the total length of a basic-shape or path motion path.
- [CSS `conic-gradient()`](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Values/gradient/conic-gradient) — color stops are placed by angle around a center point.
- [CSS masking](https://developer.mozilla.org/en-US/docs/Web/CSS/Guides/Masking) — gradients and SVG masks can restrict paint to a border-shaped region.
- [SVG Strokes specification](https://svgwg.org/specs/strokes/#Dashing) — dash arrays define painted and unpainted lengths along a path, and dash offset moves the pattern along that path.
- [SVG `stroke-dasharray`](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/stroke-dasharray) — dash patterns continue through rectangle corners.
- [SVG `stroke-dashoffset`](https://developer.mozilla.org/en-US/docs/Web/SVG/Reference/Attribute/stroke-dashoffset) — the dash offset is animatable and applies to SVG `<rect>` elements.
