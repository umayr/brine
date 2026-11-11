import { Compartment, EditorState } from '@codemirror/state';
import { defaultKeymap, history, historyKeymap, indentLess, indentMore } from '@codemirror/commands';
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete';
import { bracketMatching, HighlightStyle, foldGutter, foldKeymap, indentOnInput, syntaxHighlighting } from '@codemirror/language';
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search';
import {
  EditorView,
  Decoration,
  ViewPlugin,
  crosshairCursor,
  drawSelection,
  dropCursor,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from '@codemirror/view';
import { pkl } from 'codemirror-lang-pkl';
import { tags } from '@lezer/highlight';

const brineHighlight = HighlightStyle.define([
  { tag: tags.comment, color: 'var(--syntax-comment)' },
  { tag: [tags.keyword, tags.operator], color: 'var(--syntax-keyword)' },
  { tag: tags.string, color: 'var(--syntax-string)' },
  { tag: [tags.number, tags.bool, tags.null], color: 'var(--syntax-number)' },
  { tag: [tags.typeName, tags.className], color: 'var(--syntax-type)' },
  { tag: tags.annotation, color: 'var(--syntax-annotation)' },
  { tag: tags.punctuation, color: 'var(--syntax-punctuation)' },
]);

const pklTerms = [
  'amends', 'import', 'config', 'engine', 'services', 'image', 'command',
  'environment', 'ports', 'volumes', 'dependsOn', 'healthcheck',
  'labels', 'pullPolicy', 'restart', 'user', 'containerName', 'networkMode',
  'init', 'capAdd', 'devices', 'cpuset', 'memLimit', 'memReservation', 'cpus',
  'cpuShares', 'pidsLimit', 'target', 'published', 'protocol', 'hostIp',
  'type', 'source', 'readOnly', 'externalVolume', 'name',
  'test', 'interval', 'timeout', 'retries', 'startPeriod', 'List', 'Mapping', 'new',
];

const baseCompletions = pklTerms.map(label => ({
  label,
  type: /^[A-Z]/.test(label) ? 'class' : 'property',
}));

function snippetPlugin(snippets) {
  const bodies = snippets
    .map(s => (s.body || s.apply || '').trim())
    .filter(body => body.length > 0)
    .sort((a, b) => b.length - a.length);

  return ViewPlugin.fromClass(class {
    constructor(view) {
      this.decorations = this.build(view);
    }

    update(update) {
      if (update.docChanged || update.viewportChanged) {
        this.decorations = this.build(update.view);
      }
    }

    build(view) {
      if (bodies.length === 0) return Decoration.none;

      const marks = [];
      const doc = view.state.doc.toString();
      for (const body of bodies) {
        let from = doc.indexOf(body);
        while (from !== -1) {
          marks.push(Decoration.mark({ class: 'cm-brine-snippet' }).range(from, from + body.length));
          from = doc.indexOf(body, from + body.length);
        }
      }
      return Decoration.set(marks, true);
    }
  }, {
    decorations: v => v.decorations,
  });
}

function pklCompletionSource(snippets) {
  const snippetCompletions = snippets.map(snippet => ({
    label: snippet.name,
    type: 'text',
    detail: 'snippet',
    info: snippet.description || snippet.category || 'Reusable Pkl fragment',
    apply: snippet.body,
  }));
  const options = [...baseCompletions, ...snippetCompletions];

  return context => {
    const word = context.matchBefore(/[A-Za-z_][\w-]*/);
    if (!word && !context.explicit) return null;
    return {
      from: word ? word.from : context.pos,
      options,
      validFor: /^[A-Za-z_][\w-]*$/,
    };
  };
}

export function createBrinePklEditor(textarea, options = {}) {
  const snippets = options.snippets || [];
  const colorTheme = new Compartment();
  const minHeight = options.minHeight || 'var(--editor-min-height)';
  const theme = EditorView.theme({
    '&': {
      borderRadius: '0 0 var(--radius-control) var(--radius-control)',
      border: 'var(--border-width) solid var(--editor-border)',
      backgroundColor: 'var(--editor-bg)',
      color: 'var(--editor-text)',
      fontSize: 'var(--font-size-base)',
    },
    '.cm-scroller': {
      fontFamily: 'var(--font-family-code)',
      minHeight,
      lineHeight: 'var(--line-height)',
    },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
      backgroundColor: 'var(--editor-selection)',
    },
    '.cm-cursor, .cm-dropCursor': {
      borderLeftColor: 'var(--color-accent)',
    },
    '.cm-content': {
      minHeight,
      padding: 'var(--space-3-5) 0',
    },
    '.cm-gutters': {
      backgroundColor: 'var(--editor-bg)',
      borderRight: 'var(--border-width) solid var(--editor-border)',
      color: 'var(--editor-muted)',
    },
    '.cm-activeLine, .cm-activeLineGutter': {
      backgroundColor: 'var(--editor-active-line)',
    },
    '.cm-tooltip': {
      border: 'var(--border-width) solid var(--editor-border)',
      backgroundColor: 'var(--editor-bg)',
      color: 'var(--editor-text)',
      borderRadius: 'var(--radius-panel)',
      overflow: 'hidden',
    },
    '.cm-tooltip-autocomplete ul li[aria-selected]': {
      backgroundColor: 'var(--color-accent)',
      color: 'var(--color-accent-contrast)',
    },
    '.cm-brine-snippet': {
      backgroundColor: 'var(--editor-snippet)',
      outline: 'var(--border-width) solid var(--editor-selection)',
      borderRadius: 'var(--radius-sm)',
    },
  });

  const view = new EditorView({
    state: EditorState.create({
      doc: textarea.value,
      extensions: [
        EditorView.contentAttributes.of({ 'aria-label': textarea.getAttribute('aria-label') || 'Pkl source editor' }),
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightSpecialChars(),
        history(),
        foldGutter(),
        drawSelection(),
        dropCursor(),
        EditorState.allowMultipleSelections.of(true),
        indentOnInput(),
        syntaxHighlighting(brineHighlight),
        bracketMatching(),
        closeBrackets(),
        rectangularSelection(),
        crosshairCursor(),
        highlightActiveLine(),
        highlightSelectionMatches(),
        keymap.of([
          { key: 'Tab', run: indentMore },
          { key: 'Shift-Tab', run: indentLess },
          ...closeBracketsKeymap,
          ...defaultKeymap,
          ...searchKeymap,
          ...historyKeymap,
          ...foldKeymap,
          ...completionKeymap,
        ]),
        pkl(),
        colorTheme.of(EditorView.theme({}, { dark: document.documentElement.dataset.theme === 'dark' })),
        ViewPlugin.fromClass(class {
          constructor(view) {
            this.onTheme = event => view.dispatch({ effects: colorTheme.reconfigure(EditorView.theme({}, { dark: event.detail.dark })) });
            window.addEventListener('brine:theme', this.onTheme);
          }
          destroy() { window.removeEventListener('brine:theme', this.onTheme); }
        }),
        theme,
        EditorView.lineWrapping,
        autocompletion({ override: [pklCompletionSource(snippets)] }),
        snippetPlugin(snippets),
        EditorView.updateListener.of(update => {
          if (update.docChanged) {
            textarea.value = update.state.doc.toString();
            textarea.dispatchEvent(new Event('input', { bubbles: true }));
          }
        }),
      ],
    }),
    parent: textarea.parentElement,
  });

  textarea.style.display = 'none';
  return view;
}
