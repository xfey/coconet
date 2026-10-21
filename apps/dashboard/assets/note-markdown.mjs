import { Lexer } from './vendor/marked-18.1.1.mjs';

// Use CommonMark tokenization, but create only these basic presentation nodes.
// Everything else remains literal text; no source HTML, URLs or images execute.
export function renderNoteMarkdown(target, source) {
  const doc = target.ownerDocument;
  const fragment = doc.createDocumentFragment();
  function append(parent, tokens) {
    for (const token of tokens) {
      switch (token.type) {
        case 'space': break;
        case 'text':
          if (token.tokens) append(parent, token.tokens);
          else parent.append(doc.createTextNode(token.text));
          break;
        case 'escape': parent.append(doc.createTextNode(token.text)); break;
        case 'paragraph': case 'strong': case 'em': {
          const node = doc.createElement(token.type === 'paragraph' ? 'p' : token.type);
          append(node, token.tokens); parent.append(node); break;
        }
        case 'codespan': {
          const node = doc.createElement('code'); node.textContent = token.text;
          parent.append(node); break;
        }
        case 'br': parent.append(doc.createElement('br')); break;
        case 'list': {
          const list = doc.createElement(token.ordered ? 'ol' : 'ul');
          if (token.ordered) list.start = token.start;
          for (const item of token.items) {
            const row = doc.createElement('li'); append(row, item.tokens); list.append(row);
          }
          parent.append(list); break;
        }
        default: parent.append(doc.createTextNode(token.raw));
      }
    }
  }
  try {
    append(fragment, Lexer.lex(source, { gfm: false }));
    target.replaceChildren(fragment);
  } catch {
    // Unusual or deeply nested input still stays readable and editable.
    target.textContent = source;
  }
}
