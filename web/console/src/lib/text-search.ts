/**
 * Plain-text search over an editor document.
 *
 * Substring matching rather than a regular expression, deliberately: the
 * documents this runs over are live cluster objects up to 4 MiB, and a pattern
 * an operator typed into a find box would be compiled and re-run on every
 * keystroke against all of it. A backtracking pattern over a document that size
 * does not fail, it hangs the tab — which, in the one editor a Kubernetes
 * object is edited in, would take unsaved work with it. Case folding and whole
 * word cover what the find box is actually used for.
 */

/**
 * The largest number of matches reported for one query.
 *
 * Every match becomes a painted span, and a one-character query over a large
 * ConfigMap has hundreds of thousands of them. The cap bounds the work; the
 * count is reported as truncated so the panel can say so rather than quietly
 * showing a wrong total. Replacement is not capped — see `replaceAllMatches`.
 */
export const MAX_TEXT_MATCHES = 5000;

export interface TextSearchOptions {
  caseSensitive: boolean;
  /** Matches only where the run is not surrounded by other word characters. */
  wholeWord: boolean;
}

export interface TextMatches {
  /** Offsets into the source, ascending, non-overlapping. */
  starts: number[];
  /** Length of every match, which is the query's — no pattern, no variance. */
  length: number;
  /** Set when `MAX_TEXT_MATCHES` cut the list short. */
  truncated: boolean;
}

const NO_MATCHES: TextMatches = { starts: [], length: 0, truncated: false };

function isWordCharAt(source: string, index: number): boolean {
  if (index < 0 || index >= source.length) {
    return false;
  }
  const code = source.charCodeAt(index);
  return (
    (code >= 48 && code <= 57) ||
    (code >= 65 && code <= 90) ||
    (code >= 97 && code <= 122) ||
    code === 95 ||
    // Everything above ASCII is treated as a word character: a CJK run has no
    // spaces in it, and reporting a boundary inside one would make whole-word
    // search of Chinese text mean nothing at all.
    code > 127
  );
}

/**
 * Folds case only when doing so preserves every offset.
 *
 * `toLowerCase` is not length-preserving for all of Unicode — `İ` lowercases to
 * two code units — and a shifted haystack would hand back offsets that address
 * the wrong characters of the document being edited. Where that happens the
 * search silently becomes case-sensitive, which returns fewer matches rather
 * than wrong ones.
 */
function foldable(source: string): string | null {
  const folded = source.toLowerCase();
  return folded.length === source.length ? folded : null;
}

export function findMatches(
  source: string,
  query: string,
  options: TextSearchOptions,
): TextMatches {
  if (!query) {
    return NO_MATCHES;
  }
  const folded = options.caseSensitive ? null : foldable(source);
  const haystack = folded ?? source;
  const needle = folded === null ? query : query.toLowerCase();
  if (needle.length === 0 || needle.length > haystack.length) {
    return NO_MATCHES;
  }

  const starts: number[] = [];
  let cursor = 0;
  let truncated = false;
  for (;;) {
    const at = haystack.indexOf(needle, cursor);
    if (at < 0) {
      break;
    }
    if (options.wholeWord && !isWholeWord(haystack, at, needle.length)) {
      // One character on, not past the run: a rejected position can still hold
      // the start of an accepted one, as `abab` does for `ab` at offset 2.
      cursor = at + 1;
      continue;
    }
    if (starts.length >= MAX_TEXT_MATCHES) {
      truncated = true;
      break;
    }
    starts.push(at);
    cursor = at + needle.length;
  }
  return { starts, length: needle.length, truncated };
}

function isWholeWord(source: string, start: number, length: number): boolean {
  return !isWordCharAt(source, start - 1) && !isWordCharAt(source, start + length);
}

/**
 * Replaces every match in one pass.
 *
 * Uncapped, unlike `findMatches`: "replace all" that stopped at five thousand
 * would leave a document half-rewritten and look finished, which is the worst
 * outcome available here. It rescans the source rather than reusing a match
 * list so the two can never disagree.
 */
export function replaceAllMatches(
  source: string,
  query: string,
  options: TextSearchOptions,
  replacement: string,
): string {
  if (!query) {
    return source;
  }
  const folded = options.caseSensitive ? null : foldable(source);
  const haystack = folded ?? source;
  const needle = folded === null ? query : query.toLowerCase();
  if (needle.length === 0) {
    return source;
  }

  let out = "";
  let copied = 0;
  let cursor = 0;
  for (;;) {
    const at = haystack.indexOf(needle, cursor);
    if (at < 0) {
      break;
    }
    if (options.wholeWord && !isWholeWord(haystack, at, needle.length)) {
      cursor = at + 1;
      continue;
    }
    out += source.slice(copied, at) + replacement;
    copied = at + needle.length;
    cursor = copied;
  }
  return copied === 0 ? source : out + source.slice(copied);
}

/** The zero-based line the offset sits on, for scrolling a match into view. */
export function lineOfOffset(source: string, offset: number): number {
  let line = 0;
  for (let index = 0; index < offset && index < source.length; index += 1) {
    if (source.charCodeAt(index) === 10) {
      line += 1;
    }
  }
  return line;
}

/** The offset's distance, in characters, from the start of its own line. */
export function columnOfOffset(source: string, offset: number): number {
  return offset - (source.lastIndexOf("\n", offset - 1) + 1);
}
