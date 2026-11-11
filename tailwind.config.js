// Brine owns the component styles and theme. Tailwind provides layout utilities.
const fs = require('node:fs');
const path = require('node:path');
// Go/HTMX fragments interpolate variants. Keep the complete authored component
// API, not just the class names Tailwind happens to find in current templates.
const components = [...new Set((fs.readFileSync(path.join(__dirname, 'internal/web/assets/app.css'), 'utf8')
  .match(/\.ui-[a-z][a-z0-9-]*/g) || []).map(selector => selector.slice(1)))];
const color = token => ({ opacityValue }) => opacityValue === undefined
  ? `var(${token})`
  : `color-mix(in srgb, var(${token}) calc(${opacityValue} * 100%), transparent)`;
const spacing = Object.fromEntries([
  '0.5','1','1.5','2','2.5','3','3.5','4','5','6','7','8','9','10','11','12',
  '14','16','20','24','28','32','40','48','52','56','64','72','80','96',
].map(size => [size, `var(--space-${size.replace('.', '-')})`]));
module.exports = {
  safelist: components,
  content: [
    './internal/web/templates/**/*.html',
    './internal/web/static/*.js',
    './internal/web/*.go',
    './internal/auth/*.go',
  ],
  theme: {
    extend: {
      colors: {
        canvas: color('--color-bg'), panel: color('--color-panel'), raised: color('--color-raised'),
        line: color('--color-line'), foreground: color('--color-text'), muted: color('--color-muted'),
        dim: color('--color-dim'), accent: color('--color-accent'),
        success: color('--status-success'), warning: color('--status-warning'),
        error: color('--status-error'), info: color('--status-info'),
      },
      spacing,
      fontFamily: { sans: ['var(--font-family)'], mono: ['var(--font-family-code)'] },
      fontSize: Object.fromEntries(['xs','sm','base','lg','xl','2xl','3xl'].map(name=>[name,`var(--font-size-${name})`])),
      fontWeight: { normal: 'var(--font-weight-normal)', medium: 'var(--font-weight-medium)', semibold: 'var(--font-weight-semibold)', bold: 'var(--font-weight-semibold)' },
      borderRadius: { sm: 'var(--radius-sm)', DEFAULT: 'var(--radius-control)', md: 'var(--radius-md)', lg: 'var(--radius-lg)', xl: 'var(--radius-xl)', panel: 'var(--radius-panel)' },
      borderWidth: { DEFAULT: 'var(--border-width)' },
      boxShadow: { sm: 'var(--shadow-panel)', DEFAULT: 'var(--shadow-panel)', lg: 'var(--shadow-toast)' },
    },
  },
  plugins: [],
};
