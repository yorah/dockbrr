# Frontend stack decisions (resolved Phase 7, user-confirmed)

Settled frontend stack for a table-driven self-hosted admin dashboard. All choices below are settled, do not re-litigate in new plans.

- **Styling/components**: Tailwind v4 (`@tailwindcss/vite`) + Radix primitives + CVA + clsx + tailwind-merge (shadcn pattern, components copied into `web/src/components/ui/`) + next-themes + lucide-react + sonner.
- **Routing**: @tanstack/react-router, code-based routes (no file-based plugin), Register augmentation in `router.tsx`.
- **Data**: TanStack Query 5 (hooks in `web/src/hooks/`, invalidation matrix documented there) + TanStack Table 8.
- **Forms**: TanStack Form 1 (simple screens use plain useState).
- **Markdown**: react-markdown + rehype-sanitize. Never rehype-raw, never dangerouslySetInnerHTML.
- **Testing**: vitest 4 + @testing-library + jsdom + msw.
- **Self-contained**: no CDN, system font stack (CSP + air-gap requirement).
- **Build integration**: `npm run build` → `web/dist/` → `//go:embed all:dist` in `internal/httpapi/spa.go`. Only `dist/index.html` placeholder is tracked; `dist/assets/` gitignored.
- **tsconfig**: 3-file layout (solution stub + tsconfig.app.json + tsconfig.node.json): TS6 rejects the 2-file layout.

Backend job-status vocabulary (frontend must match): `queued|running|success|failed|canceled`: job statuses. `succeeded/apply_failed/rolled_back` are service-EVENT kinds, not job statuses. Whole-branch review c24242e fixed a mismatch here once already.
