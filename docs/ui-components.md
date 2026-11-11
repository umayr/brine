# Production UI components and theme

Brine's production UI uses Go templates, HTMX, Tailwind utilities and Brine-owned component classes. DaisyUI is not a runtime or build dependency. Edit `internal/web/static/theme.css` to customize the served `/static/theme.css`; it loads before `/static/vendor/app.css`. Rebuild the web assets after changing the component CSS or Tailwind configuration.

## Appearance settings

Open **Settings → Appearance** at `/settings` using the sidebar gear. The normal authentication middleware protects the route when auth is enabled. Rendering settings does not contact the engine, evaluate stack source, or expose server configuration.

- Choose System/Light/Dark, one of six accent swatches, and Comfortable/Compact table spacing. Native radio buttons support Tab, arrow keys and Space; selection is also labeled in text.
- The live preview uses actual production components. Its status labels and table rows are explicitly examples, not runtime observations; semantic colors remain independent of the accent.
- Preferences use localStorage keys `theme`, `brine-accent`, and `brine-density`, shared across tabs on the same origin. They are browser/origin preferences, not account or server settings. Changes apply immediately; there is no server save endpoint.
- Reset removes only those three keys, restoring System/Sage/Comfortable. It does not clear unrelated local storage, credentials or drafts.
- If storage is disabled or writes fail, the controls still work in memory. A warning explains that changes are limited to the current tab and will not survive reloading.
- Other open tabs update through storage events, including editor themes without replacing unsaved editor content. The Settings link stays pinned in the desktop sidebar and remains a labeled gear alongside the horizontally scrolling mobile navigation.

Table density changes padding, not text size or control targets. Configure normal and small-table spacing through `--table-padding-y`, `--table-padding-y-sm`, `--table-padding-y-compact`, and `--table-padding-y-sm-compact` in `theme.css`. Appearance card/swatch dimensions use the `--appearance-*` variables in the same file.

## Theme and tokens

The appearance control offers system, light and dark. `theme.js` applies `data-theme="light"` or `data-theme="dark"` to the document root, tracks system changes, synchronizes storage changes, and accepts the old stored values `night` and `corporate` for migration. `setBrineTheme(value)` changes the preference. The independent accent control calls `setBrineAccent(value)` with `sage`, `glacier`, `lilac`, `sand`, `rose` or `graphite`, reflected in `data-accent`. `setBrineDensity(value)` accepts `comfortable` or `compact` and selects `data-density`; `resetBrinePreferences()` restores the browser defaults. The `brine:theme` event includes resolved `dark`, theme `preference`, `accent` and `density` values.

Edit the plain CSS custom properties rather than generated vendor CSS:

- Surfaces and text: `--color-bg`, `--color-panel`, `--color-raised`, `--color-line`, `--color-text`, `--color-muted`, `--color-dim`.
- Accent: `--color-accent`, `--color-accent-contrast`. Semantic status colors `--status-success`, `--status-warning`, `--status-error`, `--status-info` are independent of accent.
- Typography: `--font-family`, `--font-family-code`, `--font-size-root`, `--font-size-base`, `--font-size-sm`, `--font-size-xs`. Root size establishes the rem scale; body and editor use the base size without compounding it.
- Shape and sizing: `--radius-control`, `--radius-panel`, `--radius-dialog`, `--control-height`, `--control-height-sm`, `--control-height-xs`, `--sidebar-width`, `--header-height`, `--content-padding`, and the `--space-*` scale.
- Editor aliases: `--editor-bg`, `--editor-text`, `--editor-muted`, `--editor-border`, plus `--syntax-*` highlighting and `--editor-*-min-height` sizing.
- Dialog and feedback: `--dialog-width`, `--dialog-padding`, `--toast-width`, `--toast-duration` (CSS time, e.g. `4s` or `4000ms`).
- Icons: `--icon-stroke` controls inline Lucide outlines. Status masks are local SVG assets selected through `--symbol-*`; replace these URLs to change their shapes.

`--color-accent` normally references `--selected-accent`, which the six palette selectors set from `--accent-sage`, `--accent-glacier`, etc. Edit those palette values to retain the selector, or replace `--color-accent` with a fixed color to pin every palette to your own accent. Adjust `--color-accent-contrast` for readable button text. Light-theme overrides are in the same file.

No Tailwind regeneration is needed for variable-only edits. Static assets are embedded in the Go binary, so shipping a changed file requires rebuilding/replacing the binary; this is not a runtime disk-theme loader.

CSS media query breakpoints remain literal: custom properties cannot be used as media query conditions. Check both light and dark contrast when changing palettes.

## Class conventions

Use `ui-panel` for a bordered surface (padding remains a structural utility). Use `ui-button` with `ui-button-primary`, `ui-button-secondary`, `ui-button-ghost`, `ui-button-outline` or `ui-button-danger`, and `ui-button-sm`/`ui-button-xs` where appropriate. Use `ui-status` with semantic variants such as `ui-status-success`, `ui-status-warning`, `ui-status-error`, `ui-status-info`, `ui-status-running`, `ui-status-unknown` and `ui-status-queued`; status icons are decorative CSS masks. Notices use `ui-notice` and a semantic variant.

Controls use `ui-input`, `ui-textarea`, `ui-select`, `ui-checkbox` and `ui-switch`; fields use `ui-field`, `ui-label`, `ui-label-text` and `ui-label-hint`. Tables use `ui-table`; loading indicators use `ui-spinner` while retaining `htmx-indicator` when HTMX controls visibility. Dialogs use native `<dialog>` with `ui-dialog`, `ui-dialog-panel`, `ui-dialog-actions` and `ui-dialog-backdrop`; preserve accessible title associations, native focus and close behavior. Toast containers use `ui-toasts` and their positional variants. These are class names, not replacements for native HTML elements.

Use semantic Tailwind colors such as `bg-canvas`, `bg-panel`, `bg-raised`, `text-foreground`, `text-muted`, `text-dim`, `border-line` and `text-accent`. Do not use low-opacity foreground utilities for readable text; check muted/dim colors against both canvas and panel. Keep status meaning independent of the selected accent. Preserve existing form names, IDs, HTMX targets, CSRF handling and escaping when updating templates.

## Checks

```sh
npm ci
npm run build:web
npm run test:components
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

`test:components` checks source/dependency contracts, the built component API, preference behavior in a small JavaScript VM harness, and shared UI helpers without an engine, Dill or network. It covers legacy/invalid values, system changes, persistence/reset, storage synchronization/failures, split stream markers and safe file-field creation. Tailwind keeps every authored `ui-*` selector, including variants dynamically interpolated by Go/HTMX; do not let an unused-looking success/error notice disappear from built CSS. CI runs these standalone checks after rebuilding assets.

Go tests cover template parsing/execution, engine-independent settings rendering, route authentication, embedded assets, unavailable dashboard data, and escaped sign-in/recipe fragments. These checks are not a complete accessibility audit, visual verification or real lifecycle integration test. Check keyboard interactions, responsive layouts and light/dark contrast manually when changing components.

## Dependency maintenance

The frontend no longer depends on DaisyUI or CodeMirror's fixed One Dark theme. The direct esbuild range is `^0.28.2`; the lockfile also retains compatible security updates for PostCSS (8.5.32), postcss-selector-parser (6.1.6), nanoid (3.3.20), and source-map-js (1.2.2), retaining Tailwind 3. Run `npm audit --audit-level=low` alongside clean installs when updating dependencies. An audit is not a comprehensive application-security assessment.
