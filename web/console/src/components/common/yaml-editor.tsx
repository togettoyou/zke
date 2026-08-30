import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  CaseSensitive,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  Replace,
  ReplaceAll,
  Search,
  WholeWord,
  X,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { highlightYaml, type YamlTokenKind } from "@/lib/yaml-highlight";
import {
  columnOfOffset,
  findMatches,
  lineOfOffset,
  replaceAllMatches,
  type TextMatches,
  type TextSearchOptions,
} from "@/lib/text-search";

/**
 * Beyond this, highlighting is dropped rather than the editor becoming slow.
 *
 * The Server accepts documents up to 4 MiB, and tokenizing one of those on
 * every keystroke would cost far more than the colour is worth. A Kubernetes
 * object large enough to cross this line is a ConfigMap or a CRD carrying an
 * embedded file, which is exactly the content the highlighter has least to say
 * about. The text stays fully editable; only the colour goes.
 */
const MAX_HIGHLIGHT_CHARS = 200_000;

/** Avoid turning an unusually large embedded file into tens of thousands of DOM lines. */
const MAX_NUMBERED_LINES = 20_000;

const TOKEN_CLASS: Record<YamlTokenKind, string | undefined> = {
  plain: undefined,
  comment: "text-code-comment",
  key: "text-code-key",
  string: "text-code-string",
  literal: "text-code-literal",
  punctuation: "text-code-punctuation",
  meta: "text-code-meta",
};

/**
 * Metrics shared by the three layers.
 *
 * Every value here is load-bearing. The highlighted <pre> and the match
 * highlights are painted directly behind a transparent textarea, so any
 * difference in font, size, leading, padding or wrapping between them shows up
 * as colour drifting away from the characters it belongs to — worst at the
 * bottom of a long document, which is where it is least likely to be noticed in
 * a screenshot and most likely to matter to whoever is reading the file.
 */
const LAYER =
  "zke-mono absolute inset-0 h-full w-full py-2 pr-2.5 text-xs leading-relaxed whitespace-pre";

/** Measured to convert a match's column into a horizontal scroll offset. */
const WIDTH_PROBE = "0".repeat(20);

const NO_MATCHES: TextMatches = { starts: [], length: 0, truncated: false };

/** Written with both modifiers because the Console runs on macOS and on Windows. */
const FIND_HINT = "查找（Ctrl/⌘ + F）";
const REPLACE_HINT = "查找并替换（Ctrl/⌘ + H）";

/**
 * A YAML text editor with syntax highlighting.
 *
 * A textarea rather than a full editor component: this is the surface an
 * operator edits a live cluster object in, and the browser's own text control
 * is the one thing guaranteed to keep native selection, undo, IME composition,
 * spell-check settings and screen-reader behaviour intact. The colour is a
 * layer underneath it, not a replacement for it — which also means a bug in the
 * highlighter can lose colours but can never lose or alter a keystroke.
 *
 * It carries its own find and replace. The browser's `Ctrl`/`Cmd`+`F` cannot
 * see inside a textarea's value at all, so in this one control the shortcut
 * every operator already has in their fingers finds nothing — which is worse
 * than having no shortcut, because it reads as "the text is not there". The
 * panel takes that key over and answers it, and read-only documents get find
 * without replace.
 */
export function YamlEditor({
  value,
  onChange,
  readOnly = false,
  label,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  readOnly?: boolean;
  /** Accessible name for the text control. */
  label: string;
  /** Sizing for the frame; the layers inside always fill it. */
  className?: string;
}) {
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const paintedRef = useRef<HTMLSpanElement>(null);
  const gutterRef = useRef<HTMLSpanElement>(null);
  const matchLayerRef = useRef<HTMLSpanElement>(null);
  const probeRef = useRef<HTMLSpanElement>(null);
  const queryRef = useRef<HTMLInputElement>(null);

  const [findOpen, setFindOpen] = useState(false);
  const [replaceShown, setReplaceShown] = useState(false);
  const [query, setQuery] = useState("");
  const [replacement, setReplacement] = useState("");
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [wholeWord, setWholeWord] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  // Bumped whenever the active match should be scrolled to. A counter rather
  // than an effect on the match itself: editing the document moves every offset
  // after the caret, and revealing on that would drag the view away from what
  // is being typed.
  const [reveal, setReveal] = useState(0);
  // Same idea for the query field: re-pressing the shortcut with the panel
  // already open has to re-select it, which no state change of its own implies.
  const [focusQuery, setFocusQuery] = useState(0);

  const options: TextSearchOptions = useMemo(
    () => ({ caseSensitive, wholeWord }),
    [caseSensitive, wholeWord],
  );

  const lines = useMemo(
    () => (value.length <= MAX_HIGHLIGHT_CHARS ? highlightYaml(value) : null),
    [value],
  );
  const lineNumbers = useMemo(() => buildLineNumbers(value), [value]);

  // Only while the panel is open: a closed find box must cost the document
  // nothing, and leaving highlights behind after it closes would paint a
  // decision the operator has already dismissed.
  const matches = useMemo(
    () => (findOpen && query ? findMatches(value, query, options) : NO_MATCHES),
    [findOpen, query, value, options],
  );
  const active = matches.starts.length > 0 ? Math.min(activeIndex, matches.starts.length - 1) : -1;
  const matchSegments = useMemo(
    () => buildMatchSegments(value, matches, active),
    [value, matches, active],
  );

  // The painted text is offset to wherever the textarea has been scrolled to.
  //
  // A transform rather than the layer's own `scrollTop`/`scrollLeft`: only the
  // textarea reserves layout space for its scrollbars, so its client box is
  // shorter and narrower than the layer's and it can scroll that much further.
  // Assigning its offset to a scroll position would be clamped to the layer's
  // own smaller maximum, leaving the colour a scrollbar's width behind the
  // characters at — and only at — the very end of the document. A transform has
  // no maximum to be clamped to.
  const syncScroll = useCallback(() => {
    const input = inputRef.current;
    if (!input) {
      return;
    }
    const offset = `translate(${-input.scrollLeft}px, ${-input.scrollTop}px)`;
    if (paintedRef.current) {
      paintedRef.current.style.transform = offset;
    }
    if (matchLayerRef.current) {
      matchLayerRef.current.style.transform = offset;
    }
    if (gutterRef.current) {
      gutterRef.current.style.transform = `translateY(${-input.scrollTop}px)`;
    }
  }, []);

  // Text replaced from outside — a reload, a discarded edit — can move the
  // textarea's scroll offset without a scroll event the layer would see.
  // Before paint, so the layers are never shown disagreeing.
  useLayoutEffect(syncScroll, [value, matchSegments, syncScroll]);

  /**
   * Anything that changes what counts as a match starts over from the first
   * one: an ordinal was a statement about the previous query and means nothing
   * about this one. Called from the handlers rather than from an effect on the
   * query, so the reveal happens once per interaction instead of once per
   * render that happens to carry a different query.
   */
  const restartSearch = useCallback(() => {
    setActiveIndex(0);
    setReveal((token) => token + 1);
  }, []);

  const openFind = useCallback(
    (withReplace: boolean) => {
      const input = inputRef.current;
      // A selection inside the document is what the operator has already told
      // us they are looking at, so it seeds the query — but only a single line
      // of it, because a multi-line selection is a block being moved, not a
      // term being searched for.
      if (input) {
        const selected = value.slice(input.selectionStart, input.selectionEnd);
        if (selected && !selected.includes("\n") && selected.length <= 200) {
          setQuery(selected);
        }
      }
      setFindOpen(true);
      if (withReplace && !readOnly) {
        setReplaceShown(true);
      }
      setFocusQuery((token) => token + 1);
      restartSearch();
    },
    [readOnly, restartSearch, value],
  );

  const closeFind = useCallback(() => {
    setFindOpen(false);
    inputRef.current?.focus();
  }, []);

  const step = useCallback(
    (delta: number) => {
      const count = matches.starts.length;
      if (count === 0) {
        return;
      }
      setActiveIndex((((active + delta) % count) + count) % count);
      setReveal((token) => token + 1);
    },
    [active, matches.starts.length],
  );

  /** Rewrites one match and aims navigation at the next one after the edit. */
  const replaceActive = useCallback(() => {
    const start = active < 0 ? undefined : matches.starts[active];
    if (readOnly || start === undefined) {
      return;
    }
    const next = value.slice(0, start) + replacement + value.slice(start + matches.length);
    const caret = start + replacement.length;
    // Re-found against the rewritten text rather than assuming the index still
    // points one match further on: a replacement may itself contain the query,
    // in which case leaving the index alone would replace the same run forever.
    const after = findMatches(next, query, options);
    const index = after.starts.findIndex((offset) => offset >= caret);
    onChange(next);
    setActiveIndex(index < 0 ? 0 : index);
    setReveal((token) => token + 1);
  }, [active, matches, onChange, options, query, readOnly, replacement, value]);

  const replaceEvery = useCallback(() => {
    if (readOnly || matches.starts.length === 0) {
      return;
    }
    onChange(replaceAllMatches(value, query, options, replacement));
    setActiveIndex(0);
    setReveal((token) => token + 1);
  }, [matches.starts.length, onChange, options, query, readOnly, replacement, value]);

  useEffect(() => {
    if (focusQuery === 0) {
      return;
    }
    const field = queryRef.current;
    field?.focus();
    field?.select();
  }, [focusQuery]);

  // Scrolls the active match into view, and selects it in the textarea so that
  // clicking back into the document lands on what was being looked at.
  useEffect(() => {
    const input = inputRef.current;
    const start = active < 0 ? undefined : matches.starts[active];
    if (reveal === 0 || !input || start === undefined) {
      return;
    }
    const end = start + matches.length;
    input.setSelectionRange(start, end);

    const style = window.getComputedStyle(input);
    const lineHeight = Number.parseFloat(style.lineHeight) || 16;
    const paddingTop = Number.parseFloat(style.paddingTop) || 0;
    const paddingLeft = Number.parseFloat(style.paddingLeft) || 0;

    const top = paddingTop + lineOfOffset(value, start) * lineHeight;
    if (top < input.scrollTop) {
      input.scrollTop = Math.max(0, top - lineHeight * 3);
    } else if (top + lineHeight > input.scrollTop + input.clientHeight) {
      input.scrollTop = top - input.clientHeight + lineHeight * 3;
    }

    // Monospace, so one measured character stands for all of them. Wide glyphs
    // in a quoted string make this an approximation; it is only ever used to
    // decide how far to scroll, never to place the highlight, which is laid out
    // by the browser from the same text.
    const probe = probeRef.current?.getBoundingClientRect().width ?? 0;
    const charWidth = probe / WIDTH_PROBE.length;
    if (charWidth > 0) {
      const left = paddingLeft + columnOfOffset(value, start) * charWidth;
      const right = left + matches.length * charWidth;
      if (left < input.scrollLeft + paddingLeft) {
        input.scrollLeft = Math.max(0, left - paddingLeft - charWidth * 8);
      } else if (right > input.scrollLeft + input.clientWidth) {
        input.scrollLeft = right - input.clientWidth + charWidth * 8;
      }
    }
    syncScroll();
    // Deliberately keyed on the reveal token alone: the values read here are the
    // current render's, and re-running on every keystroke is the yank this
    // counter exists to avoid.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal]);

  /**
   * Keyboard entry for the whole editor.
   *
   * On the frame rather than on the textarea, so the shortcuts keep working
   * while the caret is in the find or replace field. `event.code` rather than
   * `event.key`: on macOS `Option` rewrites the character a key produces, and
   * `Cmd`+`Option`+`F` arrives as `ƒ`.
   */
  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const mod = event.metaKey || event.ctrlKey;
    if (mod && event.code === "KeyF" && !event.shiftKey) {
      event.preventDefault();
      openFind(event.altKey);
      return;
    }
    if (mod && event.code === "KeyH" && !event.altKey) {
      // Taken even on a read-only document: left alone it opens the browser's
      // history sidebar over the object being read, and find without replace is
      // a far better answer to that key here.
      event.preventDefault();
      openFind(true);
      return;
    }
    if (event.key === "F3" || (mod && event.code === "KeyG")) {
      event.preventDefault();
      if (!findOpen) {
        openFind(false);
        return;
      }
      step(event.shiftKey ? -1 : 1);
      return;
    }
    if (event.key === "Escape" && findOpen) {
      event.preventDefault();
      // Stops here rather than reaching the dialog or window this editor may be
      // inside: dismissing the find box is what Escape means while it is open.
      event.stopPropagation();
      closeFind();
    }
  };

  const matchCount = matches.starts.length;
  const counter = query
    ? matchCount === 0
      ? "无结果"
      : `${active + 1}/${matchCount}${matches.truncated ? "+" : ""}`
    : "";

  return (
    <div
      onKeyDown={handleKeyDown}
      className={cn(
        "zke-focus-within border-border bg-surface rounded-control shadow-e1 hover:border-border-strong relative overflow-hidden border transition-[border-color,box-shadow] duration-150",
        className,
      )}
    >
      <span
        ref={probeRef}
        aria-hidden="true"
        className="zke-mono invisible absolute top-0 left-0 text-xs leading-relaxed whitespace-pre"
      >
        {WIDTH_PROBE}
      </span>

      {lineNumbers ? (
        <pre
          aria-hidden="true"
          className="border-border bg-surface-muted/90 text-subtle-foreground zke-mono pointer-events-none absolute inset-y-0 left-0 z-10 w-12 overflow-hidden border-r py-2 pr-2 text-right text-xs leading-relaxed whitespace-pre select-none"
        >
          <span ref={gutterRef} className="zke-tnum block">
            {lineNumbers}
          </span>
        </pre>
      ) : null}

      {matchSegments ? (
        /* Under the syntax layer, so a highlighted run keeps its colours and
           only gains a background. The text is the document's own, transparent
           — laid out by the browser from the same string with the same metrics,
           which is what keeps every highlight on its characters without a single
           coordinate being computed here. */
        <pre
          aria-hidden="true"
          className={cn(
            LAYER,
            "pointer-events-none overflow-hidden text-transparent",
            lineNumbers ? "pl-14" : "pl-2.5",
          )}
        >
          <span ref={matchLayerRef} className="block">
            {matchSegments.map((segment, index) => (
              <span
                key={index}
                className={
                  segment.kind === "active"
                    ? "bg-code-match-active rounded-inline"
                    : segment.kind === "match"
                      ? "bg-code-match rounded-inline"
                      : undefined
                }
              >
                {segment.text}
              </span>
            ))}
          </span>
        </pre>
      ) : null}

      {lines ? (
        <pre
          aria-hidden="true"
          className={cn(
            LAYER,
            "text-foreground pointer-events-none overflow-hidden",
            lineNumbers ? "pl-14" : "pl-2.5",
          )}
        >
          {/* The moved element sits inside the padded, clipping layer, so the
              text passes under the padding exactly as the textarea's own does. */}
          <span ref={paintedRef} className="block">
            {lines.map((tokens, index) => (
              <span key={index}>
                {tokens.map((token, tokenIndex) => (
                  <span key={tokenIndex} className={TOKEN_CLASS[token.kind]}>
                    {token.text}
                  </span>
                ))}
                {"\n"}
              </span>
            ))}
          </span>
        </pre>
      ) : null}

      <textarea
        ref={inputRef}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onScroll={syncScroll}
        readOnly={readOnly}
        spellCheck={false}
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="off"
        aria-label={label}
        // No wrapping, in both layers: YAML is indentation, and a soft-wrapped
        // line reads as a nesting level that is not there.
        wrap="off"
        className={cn(
          LAYER,
          "zke-code-input caret-foreground resize-none overflow-auto bg-transparent outline-none",
          lineNumbers ? "pl-14" : "pl-2.5",
          // Transparent text over the painted layer, so the caret, the
          // selection and the text itself all stay exactly where the browser
          // put them. Without a layer to paint, the textarea shows its own text.
          lines ? "text-transparent" : "text-foreground",
        )}
      />

      {findOpen ? (
        <SearchPanel
          query={query}
          onQueryChange={(next) => {
            setQuery(next);
            restartSearch();
          }}
          replacement={replacement}
          onReplacementChange={setReplacement}
          replaceShown={replaceShown && !readOnly}
          onToggleReplace={() => setReplaceShown((shown) => !shown)}
          readOnly={readOnly}
          caseSensitive={caseSensitive}
          onToggleCase={() => {
            setCaseSensitive((on) => !on);
            restartSearch();
          }}
          wholeWord={wholeWord}
          onToggleWholeWord={() => {
            setWholeWord((on) => !on);
            restartSearch();
          }}
          counter={counter}
          hasMatches={matchCount > 0}
          onStep={step}
          onReplaceActive={replaceActive}
          onReplaceEvery={replaceEvery}
          onClose={closeFind}
          queryRef={queryRef}
        />
      ) : (
        /* Always on screen rather than on hover: a touch device has no hover
           state to reveal it in, and this is the only way in to find there. */
        <Button
          type="button"
          variant="secondary"
          size="icon-sm"
          onClick={() => openFind(false)}
          aria-label={FIND_HINT}
          title={FIND_HINT}
          className="bg-surface/85 text-muted-foreground hover:text-foreground absolute top-2 right-3 z-20 opacity-75 backdrop-blur-sm hover:opacity-100"
        >
          <Search />
        </Button>
      )}
    </div>
  );
}

/**
 * The find and replace panel.
 *
 * Floating over the document rather than stacked above it, for the same reason
 * every editor puts it there: the frame is given a height by whoever placed the
 * editor, and a bar that pushed the text down would shorten the document by its
 * own height every time it opened.
 */
function SearchPanel({
  query,
  onQueryChange,
  replacement,
  onReplacementChange,
  replaceShown,
  onToggleReplace,
  readOnly,
  caseSensitive,
  onToggleCase,
  wholeWord,
  onToggleWholeWord,
  counter,
  hasMatches,
  onStep,
  onReplaceActive,
  onReplaceEvery,
  onClose,
  queryRef,
}: {
  query: string;
  onQueryChange: (value: string) => void;
  replacement: string;
  onReplacementChange: (value: string) => void;
  replaceShown: boolean;
  onToggleReplace: () => void;
  readOnly: boolean;
  caseSensitive: boolean;
  onToggleCase: () => void;
  wholeWord: boolean;
  onToggleWholeWord: () => void;
  counter: string;
  hasMatches: boolean;
  onStep: (delta: number) => void;
  onReplaceActive: () => void;
  onReplaceEvery: () => void;
  onClose: () => void;
  queryRef: React.RefObject<HTMLInputElement | null>;
}) {
  return (
    <div
      role="search"
      aria-label="在文档中查找"
      // `data-state` drives the shared pop-in: the panel is mounted
      // conditionally rather than by Radix, so there is an entrance to animate
      // but no closing state to animate out of.
      data-state="open"
      // Capped and wrapping rather than clipped: the frame is
      // `overflow-hidden`, so in a window dragged down to a phone's width a
      // fixed-width toolbar would lose its rightmost buttons silently.
      className="zke-pop-motion border-border bg-surface-overlay shadow-e3 rounded-panel absolute top-2 right-3 z-20 flex max-w-[calc(100%-1.5rem)] items-start gap-1 border p-1 backdrop-blur-md"
    >
      {readOnly ? null : (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onToggleReplace}
          aria-expanded={replaceShown}
          aria-label={replaceShown ? "收起替换" : REPLACE_HINT}
          title={replaceShown ? "收起替换" : REPLACE_HINT}
        >
          {replaceShown ? <ChevronDown /> : <ChevronRight />}
        </Button>
      )}

      <div className="grid min-w-0 gap-1">
        <div className="flex min-w-0 flex-wrap items-center gap-1">
          <div className="relative">
            <Input
              ref={queryRef}
              value={query}
              onChange={(event) => onQueryChange(event.target.value)}
              onKeyDown={(event) => {
                // An IME is mid-composition on this Enter: it is committing a
                // candidate, not asking for the next match.
                if (event.key === "Enter" && !event.nativeEvent.isComposing) {
                  event.preventDefault();
                  onStep(event.shiftKey ? -1 : 1);
                }
              }}
              placeholder="查找"
              aria-label="查找内容"
              spellCheck={false}
              autoComplete="off"
              // Room on the right for the counter, which sits inside the field
              // so the panel does not grow a column that is empty until a query
              // is typed.
              className="zke-mono h-8 w-36 pr-14 pl-2 text-xs @md:w-52"
            />
            <span
              aria-live="polite"
              className="text-subtle-foreground zke-tnum pointer-events-none absolute inset-y-0 right-1.5 flex items-center text-[11px]"
            >
              {counter}
            </span>
          </div>

          <ToggleButton
            pressed={caseSensitive}
            onClick={onToggleCase}
            label="区分大小写"
            icon={<CaseSensitive />}
          />
          <ToggleButton
            pressed={wholeWord}
            onClick={onToggleWholeWord}
            label="全词匹配"
            icon={<WholeWord />}
          />
          <PanelButton
            onClick={() => onStep(-1)}
            disabled={!hasMatches}
            label="上一个匹配（Shift + Enter）"
            icon={<ChevronUp />}
          />
          <PanelButton
            onClick={() => onStep(1)}
            disabled={!hasMatches}
            label="下一个匹配（Enter / F3）"
            icon={<ChevronDown />}
          />
          <PanelButton onClick={onClose} label="关闭（Esc）" icon={<X />} />
        </div>

        {replaceShown ? (
          <div className="flex min-w-0 flex-wrap items-center gap-1">
            <Input
              value={replacement}
              onChange={(event) => onReplacementChange(event.target.value)}
              onKeyDown={(event) => {
                if (event.key !== "Enter" || event.nativeEvent.isComposing) {
                  return;
                }
                event.preventDefault();
                if (event.metaKey || event.ctrlKey || event.altKey) {
                  onReplaceEvery();
                  return;
                }
                onReplaceActive();
              }}
              placeholder="替换为"
              aria-label="替换内容"
              spellCheck={false}
              autoComplete="off"
              className="zke-mono h-8 w-36 px-2 text-xs @md:w-52"
            />
            <PanelButton
              onClick={onReplaceActive}
              disabled={!hasMatches}
              label="替换当前匹配（Enter）"
              icon={<Replace />}
            />
            <PanelButton
              onClick={onReplaceEvery}
              disabled={!hasMatches}
              label="全部替换（Ctrl/⌘ + Enter）"
              icon={<ReplaceAll />}
            />
          </div>
        ) : null}
      </div>
    </div>
  );
}

function PanelButton({
  onClick,
  disabled,
  label,
  icon,
}: {
  onClick: () => void;
  disabled?: boolean;
  label: string;
  icon: React.ReactNode;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      title={label}
      className="shrink-0"
    >
      {icon}
    </Button>
  );
}

function ToggleButton({
  pressed,
  onClick,
  label,
  icon,
}: {
  pressed: boolean;
  onClick: () => void;
  label: string;
  icon: React.ReactNode;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      onClick={onClick}
      aria-pressed={pressed}
      aria-label={label}
      title={label}
      className={cn("shrink-0", pressed ? "bg-primary-surface text-primary" : undefined)}
    >
      {icon}
    </Button>
  );
}

type MatchSegment = { text: string; kind: "text" | "match" | "active" };

/**
 * Splits the document into runs of matched and unmatched text.
 *
 * One segment per boundary rather than one per line: the unmatched stretches
 * stay whole strings, so a 4 MiB document with three matches is seven text
 * nodes rather than a hundred thousand.
 */
function buildMatchSegments(
  value: string,
  matches: TextMatches,
  active: number,
): MatchSegment[] | null {
  if (matches.starts.length === 0 || matches.length === 0) {
    return null;
  }
  const segments: MatchSegment[] = [];
  let cursor = 0;
  matches.starts.forEach((start, index) => {
    if (start > cursor) {
      segments.push({ text: value.slice(cursor, start), kind: "text" });
    }
    segments.push({
      text: value.slice(start, start + matches.length),
      kind: index === active ? "active" : "match",
    });
    cursor = start + matches.length;
  });
  if (cursor < value.length) {
    segments.push({ text: value.slice(cursor), kind: "text" });
  }
  return segments;
}

/** Builds one compact text node instead of one React element per line. */
function buildLineNumbers(value: string): string | null {
  let count = 1;
  for (let index = 0; index < value.length; index += 1) {
    if (value.charCodeAt(index) !== 10) {
      continue;
    }
    count += 1;
    if (count > MAX_NUMBERED_LINES) {
      return null;
    }
  }
  return Array.from({ length: count }, (_, index) => String(index + 1)).join("\n");
}
