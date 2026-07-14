# Brand assets

The mark is a container crate with an update badge. The one thing dockbrr does, in one shape.

## Files

| File | Use |
| --- | --- |
| `mark.svg` | The mark, no ground. Docs, README, anywhere the background is known-light or known-dark. |
| `tile.svg` | The mark on a navy tile. Avatars, Docker Hub, iOS home screen, anywhere transparency composites onto a ground we don't control. |
| `../../web/public/favicon.svg` | Browser tab. Identical to `mark.svg`; keep the two in sync when either changes. |

## Colours

These are the app's tokens, not a separate brand palette. The icon cannot drift from the UI.

| Token | Hex | Role in the mark |
| --- | --- | --- |
| `--primary` | `#2563eb` | Crate. Clears contrast on white and on `#020617`, which is why the mark needs no light/dark variant. |
| `--warning` | `#f59e0b` | Update badge. Same amber the dashboard uses for "update available". |
| `--info` | `#38bdf8` | Crate, **tile only**: `#2563eb` is too close to the navy ground to hold its edge. |
| `--background` (dark) | `#0f172a` | Tile ground. |

## In a GitHub README

`mark.svg` needs no `<picture>` dark-mode swap, both of its colours already clear contrast on
GitHub's light and dark grounds.

```markdown
<img src="docs/brand/mark.svg" width="72" alt="">

# dockbrr
```

## Raster exports

There are none checked in: this repo has no rasteriser, and the mark is vector everywhere it is
currently used. Two places will eventually want PNG:

- **GitHub social preview** (Settings → Social preview) wants 1280×640.
- **`apple-touch-icon`** wants a 180×180 PNG of `tile.svg`; iOS does not read SVG and flattens
  transparency to black.

Generate them with any SVG rasteriser, e.g. `rsvg-convert -w 180 -h 180 tile.svg > apple-touch-icon.png`.
