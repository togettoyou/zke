import { useLayoutEffect, useMemo, useRef } from "react";

import { cn } from "@/lib/cn";
import { highlightYaml, type YamlTokenKind } from "@/lib/yaml-highlight";

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
 * Metrics shared by the two layers.
 *
 * Every value here is load-bearing. The highlighted <pre> is painted directly
 * behind a transparent textarea, so any difference in font, size, leading,
 * padding or wrapping between them shows up as colour drifting away from the
 * characters it belongs to — worst at the bottom of a long document, which is
 * where it is least likely to be noticed in a screenshot and most likely to
 * matter to whoever is reading the file.
 */
const LAYER =
  "zke-mono absolute inset-0 h-full w-full px-2.5 py-2 text-xs leading-relaxed whitespace-pre";

/**
 * A YAML text editor with syntax highlighting.
 *
 * A textarea rather than a full editor component: this is the surface an
 * operator edits a live cluster object in, and the browser's own text control
 * is the one thing guaranteed to keep native selection, undo, IME composition,
 * spell-check settings and screen-reader behaviour intact. The colour is a
 * layer underneath it, not a replacement for it — which also means a bug in the
 * highlighter can lose colours but can never lose or alter a keystroke.
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

  const lines = useMemo(
    () => (value.length <= MAX_HIGHLIGHT_CHARS ? highlightYaml(value) : null),
    [value],
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
  const syncScroll = () => {
    const input = inputRef.current;
    const painted = paintedRef.current;
    if (!input || !painted) {
      return;
    }
    painted.style.transform = `translate(${-input.scrollLeft}px, ${-input.scrollTop}px)`;
  };

  // Text replaced from outside — a reload, a discarded edit — can move the
  // textarea's scroll offset without a scroll event the layer would see.
  // Before paint, so the two layers are never shown disagreeing.
  useLayoutEffect(syncScroll, [value]);

  return (
    <div
      className={cn(
        "zke-focus-within border-border bg-surface rounded-control shadow-e1 hover:border-border-strong relative overflow-hidden border transition-[border-color,box-shadow] duration-150",
        className,
      )}
    >
      {lines ? (
        <pre
          aria-hidden="true"
          className={cn(LAYER, "text-foreground pointer-events-none overflow-hidden")}
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
          // Transparent text over the painted layer, so the caret, the
          // selection and the text itself all stay exactly where the browser
          // put them. Without a layer to paint, the textarea shows its own text.
          lines ? "text-transparent" : "text-foreground",
        )}
      />
    </div>
  );
}
