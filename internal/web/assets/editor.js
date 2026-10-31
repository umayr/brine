import { EditorState } from '@codemirror/state';
import { defaultKeymap, history, historyKeymap, indentLess, indentMore } from '@codemirror/commands';
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete';
import { bracketMatching, defaultHighlightStyle, foldGutter, foldKeymap, indentOnInput, syntaxHighlighting } from '@codemirror/language';
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
import { oneDark } from '@codemirror/theme-one-dark';

const pklTerms = [
  'amends', 'import', 'config', 'engine', 'services', 'image', 'command',
  'environment', 'ports', 'volumes', 'depends_on', 'healthcheck', 'resources',
  'labels', 'pull_policy', 'restart', 'working_dir', 'entrypoint', 'user',
  'networks', 'secrets', 'List', 'Mapping', 'new',
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
  const minHeight = options.minHeight || '28rem';
  const theme = EditorView.theme({
    '&': {
      borderRadius: '0 0 8px 8px',
      border: '1px solid rgb(48 54 61)',
      backgroundColor: '#0d1117',
      fontSize: '13px',
    },
    '.cm-scroller': {
      fontFamily: "ui-monospace, 'Cascadia Code', 'SF Mono', Menlo, monospace",
      minHeight,
      lineHeight: '1.65',
    },
    '.cm-content': {
      minHeight,
      padding: '14px 0',
    },
    '.cm-gutters': {
      backgroundColor: '#0d1117',
      borderRight: '1px solid #30363d',
      color: '#6e7681',
    },
    '.cm-activeLine, .cm-activeLineGutter': {
      backgroundColor: 'rgba(110, 118, 129, 0.14)',
    },
    '.cm-tooltip': {
      border: '1px solid #30363d',
      backgroundColor: '#161b22',
      color: '#c9d1d9',
      borderRadius: '8px',
      overflow: 'hidden',
    },
    '.cm-tooltip-autocomplete ul li[aria-selected]': {
      backgroundColor: '#1f6feb',
      color: 'white',
    },
    '.cm-brine-snippet': {
      backgroundColor: 'rgba(46, 160, 67, 0.10)',
      outline: '1px solid rgba(46, 160, 67, 0.22)',
      borderRadius: '3px',
    },
  });

  const view = new EditorView({
    state: EditorState.create({
      doc: textarea.value,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightSpecialChars(),
        history(),
        foldGutter(),
        drawSelection(),
        dropCursor(),
        EditorState.allowMultipleSelections.of(true),
        indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
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
        oneDark,
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
