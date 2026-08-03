/**
 * A display-only YAML tokenizer.
 *
 * Deliberately not a parser: nothing here validates a document, and nothing
 * here may throw or drop input. Every byte handed in comes back out in the same
 * order, which is what lets the caller paint the result underneath a textarea
 * holding the same text — a tokenizer that normalised anything would slide the
 * colours off the characters they belong to.
 *
 * Scope is Kubernetes manifests as the Server returns them: block mappings and
 * sequences, quoted and plain scalars, comments, anchors, tags and block
 * scalars. Flow collections are coloured as punctuation plus scalars rather
 * than parsed. Rare corners of the spec — multi-line plain scalars, complex
 * `?` keys, directives — fall back to plain text instead of being guessed at,
 * because a wrong colour on a document an operator is about to apply is worse
 * than no colour.
 */

export type YamlTokenKind =
  | "plain"
  | "comment"
  | "key"
  | "string"
  /** Numbers, booleans and null — one class of "the value is a literal". */
  | "literal"
  | "punctuation"
  /** Anchors, aliases and tags: `&name`, `*name`, `!!str`. */
  | "meta";

export interface YamlToken {
  kind: YamlTokenKind;
  text: string;
}

/**
 * Tokenizes a document into one token list per line, newlines excluded.
 *
 * Splitting by line is what makes this cheap enough to run on every keystroke:
 * a line is tokenized independently apart from one carried bit of state, the
 * block scalar in progress.
 */
export function highlightYaml(source: string): YamlToken[][] {
  const state: ScanState = { blockColumn: null };
  return source.split("\n").map((line) => tokenizeLine(line, state));
}

interface ScanState {
  /**
   * Column at which the node holding an open block scalar starts, or `null`
   * outside one. Content belonging to the scalar is indented past it; the first
   * line that is not ends the scalar.
   */
  blockColumn: number | null;
}

/** A plain scalar key, quoted or not, followed by its `:`. */
const KEY = /^(?:"(?:[^"\\]|\\.)*"|'(?:''|[^'])*'|[^\s#,{}[\]&*!|>'"][^:#]*?)(?=:(?:\s|$))/;
/** `- ` opening a sequence entry, or a `-` alone on the line. */
const SEQUENCE_ENTRY = /^-(\s+|$)/;
const DOCUMENT_MARKER = /^(?:---|\.\.\.)(?=\s|$)/;
/** A block scalar header: style, then chomping and explicit indent in any order. */
const BLOCK_HEADER = /^[|>][+-]?\d?[+-]?/;
const NUMBER =
  /^[-+]?(?:0x[0-9a-fA-F]+|0o[0-7]+|\d+(?:\.\d*)?(?:[eE][-+]?\d+)?|\.\d+(?:[eE][-+]?\d+)?|\.(?:inf|nan))$/i;
/** YAML 1.1 booleans, which Kubernetes tooling still emits and accepts. */
const BOOLEANS = new Set(["true", "false", "yes", "no", "on", "off", "null"]);

function tokenizeLine(line: string, state: ScanState): YamlToken[] {
  if (line === "") {
    return [];
  }

  const indent = line.length - line.trimStart().length;

  if (state.blockColumn !== null) {
    // A blank line inside a block scalar is content, whatever its indentation
    // looks like — it carries no indentation of its own to compare.
    if (line.trim() === "" || indent > state.blockColumn) {
      return [{ kind: "string", text: line }];
    }
    state.blockColumn = null;
  }

  const out: YamlToken[] = [];
  let rest = line;

  if (indent > 0) {
    out.push({ kind: "plain", text: line.slice(0, indent) });
    rest = line.slice(indent);
  }

  if (rest.startsWith("#")) {
    out.push({ kind: "comment", text: rest });
    return out;
  }

  const marker = DOCUMENT_MARKER.exec(rest);
  if (marker) {
    out.push({ kind: "punctuation", text: marker[0] });
    rest = rest.slice(marker[0].length);
  }

  // Sequence markers are indentation as far as a block scalar is concerned, so
  // they are consumed before the column is taken. `- - a` nests, hence a loop.
  let entry = SEQUENCE_ENTRY.exec(rest);
  while (entry) {
    out.push({ kind: "punctuation", text: "-" });
    if (entry[1]) {
      out.push({ kind: "plain", text: entry[1] });
    }
    rest = rest.slice(entry[0].length);
    entry = SEQUENCE_ENTRY.exec(rest);
  }

  const column = line.length - rest.length;

  const key = KEY.exec(rest);
  if (key) {
    out.push({ kind: "key", text: key[0] });
    out.push({ kind: "punctuation", text: ":" });
    rest = rest.slice(key[0].length + 1);
  }

  tokenizeValue(rest, column, state, out);
  return out;
}

function tokenizeValue(text: string, column: number, state: ScanState, out: YamlToken[]): void {
  let index = 0;

  while (index < text.length) {
    const char = text[index];

    if (char === " " || char === "\t") {
      let end = index;
      while (end < text.length && (text[end] === " " || text[end] === "\t")) {
        end += 1;
      }
      out.push({ kind: "plain", text: text.slice(index, end) });
      index = end;
      continue;
    }

    // `#` only opens a comment at a token boundary; inside a word it is an
    // ordinary character, which is what keeps URL fragments intact.
    if (char === "#" && (index === 0 || /\s/.test(text.charAt(index - 1)))) {
      out.push({ kind: "comment", text: text.slice(index) });
      return;
    }

    if (char === '"' || char === "'") {
      const end = scanQuoted(text, index);
      out.push({ kind: "string", text: text.slice(index, end) });
      index = end;
      continue;
    }

    if (char === "|" || char === ">") {
      const header = BLOCK_HEADER.exec(text.slice(index));
      // Only a header when nothing but a comment follows it; `>` also appears
      // inside plain scalars such as shell redirections.
      if (header && /^\s*(?:#.*)?$/.test(text.slice(index + header[0].length))) {
        state.blockColumn = column;
        out.push({ kind: "punctuation", text: header[0] });
        index += header[0].length;
        continue;
      }
    }

    if (char === "&" || char === "*" || char === "!") {
      const end = scanWord(text, index);
      // A lone `*` is multiplication in someone's arguments, not an alias.
      if (end > index + 1) {
        out.push({ kind: "meta", text: text.slice(index, end) });
        index = end;
        continue;
      }
    }

    if (char === "{" || char === "}" || char === "[" || char === "]" || char === ",") {
      out.push({ kind: "punctuation", text: char });
      index += 1;
      continue;
    }

    const end = scanWord(text, index);
    const word = text.slice(index, end);
    out.push({
      kind:
        NUMBER.test(word) || BOOLEANS.has(word.toLowerCase()) || word === "~" ? "literal" : "plain",
      text: word,
    });
    index = end;
  }
}

/**
 * Returns the index just past the closing quote, or the end of the line for an
 * unterminated string — a quoted scalar may legally continue onto the next
 * line, and colouring to the end of this one is the honest approximation.
 */
function scanQuoted(text: string, start: number): number {
  const quote = text[start];
  let index = start + 1;

  while (index < text.length) {
    const char = text[index];
    if (quote === '"' && char === "\\") {
      index += 2;
      continue;
    }
    if (char === quote) {
      // `''` is an escaped quote inside a single-quoted scalar.
      if (quote === "'" && text[index + 1] === "'") {
        index += 2;
        continue;
      }
      return index + 1;
    }
    index += 1;
  }

  return text.length;
}

/** Runs to the next separator. `#` is included so a word is never split on it. */
function scanWord(text: string, start: number): number {
  let index = start;
  while (index < text.length && !/[\s,{}[\]]/.test(text.charAt(index))) {
    index += 1;
  }
  // Always consume something, or the caller loops forever on a separator it
  // decided not to handle.
  return index === start ? start + 1 : index;
}
