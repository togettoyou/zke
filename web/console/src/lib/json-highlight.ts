/**
 * The token classes a JSON document is painted in.
 *
 * The same vocabulary the YAML highlighter uses, so both surfaces resolve to
 * the one set of `--code-*` variables in `theme.css` and a document does not
 * change colour depending on which viewer it is open in.
 */
export type JsonTokenKind = "plain" | "key" | "string" | "literal" | "punctuation";

export interface JsonToken {
  kind: JsonTokenKind;
  text: string;
}

/**
 * Tokenizes JSON for display.
 *
 * It reads the already-serialized text rather than walking the value it came
 * from, because what has to be coloured is exactly what is on screen —
 * including the indentation and line breaks `JSON.stringify` chose. Walking the
 * value would mean re-implementing that layout and getting it subtly different.
 *
 * A string is a key when the next thing that is not whitespace is a colon. That
 * is the whole grammar difference worth colouring here, and it needs no
 * bracket-depth tracking to get right.
 */
export function highlightJson(source: string): JsonToken[] {
  const tokens: JsonToken[] = [];
  let index = 0;

  const push = (kind: JsonTokenKind, text: string) => {
    const previous = tokens[tokens.length - 1];
    // Merged with the run before it where the class is the same: a document of
    // a few thousand lines is a few thousand spans rather than a few hundred
    // thousand, and the browser notices the difference.
    if (previous && previous.kind === kind) {
      previous.text += text;
      return;
    }
    tokens.push({ kind, text });
  };

  while (index < source.length) {
    const character = source[index] ?? "";
    if (character === '"') {
      const end = scanString(source, index);
      push(isKeyAt(source, end) ? "key" : "string", source.slice(index, end));
      index = end;
      continue;
    }
    if (character === "-" || (character >= "0" && character <= "9")) {
      const end = scanNumber(source, index);
      push("literal", source.slice(index, end));
      index = end;
      continue;
    }
    const word = KEYWORDS.find((candidate) => source.startsWith(candidate, index));
    if (word) {
      push("literal", word);
      index += word.length;
      continue;
    }
    if (PUNCTUATION.includes(character)) {
      push("punctuation", character);
      index += 1;
      continue;
    }
    // Whitespace and anything unrecognised. Kept verbatim: the indentation is
    // most of what makes the document readable, and a viewer that dropped a
    // byte it did not understand would be showing something other than the
    // answer that came back.
    push("plain", character);
    index += 1;
  }
  return tokens;
}

const KEYWORDS = ["true", "false", "null"] as const;
const PUNCTUATION = "{}[],:";

/** Consumes one string literal, escapes included, and reports where it ends. */
function scanString(source: string, start: number): number {
  for (let index = start + 1; index < source.length; index++) {
    const character = source[index];
    if (character === "\\") {
      index++;
      continue;
    }
    if (character === '"') {
      return index + 1;
    }
  }
  // Unterminated. Returning the rest of the document keeps the caller's loop
  // moving forward, which is the only property that matters for a viewer.
  return source.length;
}

function scanNumber(source: string, start: number): number {
  let index = start;
  if (source[index] === "-") {
    index++;
  }
  while (index < source.length && /[0-9.eE+-]/.test(source[index] ?? "")) {
    index++;
  }
  return index;
}

function isKeyAt(source: string, afterString: number): boolean {
  for (let index = afterString; index < source.length; index++) {
    const character = source[index] ?? "";
    if (character === ":") {
      return true;
    }
    if (character !== " " && character !== "\t" && character !== "\n" && character !== "\r") {
      return false;
    }
  }
  return false;
}
