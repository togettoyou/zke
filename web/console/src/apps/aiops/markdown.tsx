import { Fragment, type ReactNode } from "react";

import { cn } from "@/lib/cn";

import { CopyIconButton } from "./copy";

/**
 * A small Markdown renderer for model answers.
 *
 * Written here rather than pulled in as a dependency for two reasons. The first
 * is the trust boundary: this builds React nodes and never touches
 * `dangerouslySetInnerHTML`, so a model — or a Pod log a model quoted — cannot
 * put markup, a link scheme or a script into the Console. A general Markdown
 * library would have to be paired with a sanitizer and both kept correct
 * forever. The second is scope: model answers use headings, lists, tables,
 * emphasis, inline code and fenced blocks, and that is the whole grammar
 * implemented below. Anything else renders as the literal text it was, which is
 * the honest failure mode.
 */
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cn("space-y-2 text-[13px] leading-relaxed break-words", className)}>
      {renderBlocks(text)}
    </div>
  );
}

function renderBlocks(text: string): ReactNode[] {
  const lines = text.replaceAll("\r\n", "\n").split("\n");
  const blocks: ReactNode[] = [];
  // `at` rather than indexing: the console compiles with
  // `noUncheckedIndexedAccess`, and a parser that walks a cursor past the end is
  // the normal case here rather than a bug to guard against.
  const at = (position: number) => lines[position] ?? "";
  let index = 0;
  let key = 0;
  while (index < lines.length) {
    const line = at(index);
    if (line.trim() === "") {
      index += 1;
      continue;
    }
    if (line.trimStart().startsWith("```")) {
      const language = line.trim().slice(3).trim();
      const body: string[] = [];
      index += 1;
      while (index < lines.length && !at(index).trimStart().startsWith("```")) {
        body.push(at(index));
        index += 1;
      }
      index += 1;
      blocks.push(<CodeBlock key={key++} language={language} code={body.join("\n")} />);
      continue;
    }
    if (isTableStart(at, index)) {
      const rows: string[] = [];
      const alignments = alignmentsOf(at(index + 1));
      rows.push(at(index));
      index += 2;
      while (index < lines.length && at(index).includes("|") && at(index).trim() !== "") {
        rows.push(at(index));
        index += 1;
      }
      blocks.push(renderTable(key++, rows, alignments));
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      blocks.push(
        <p key={key++} className="text-foreground pt-1 text-sm font-semibold">
          {renderInline(heading[2] ?? "")}
        </p>,
      );
      index += 1;
      continue;
    }
    if (/^\s*([-*+]|\d+\.)\s+/.test(line)) {
      const ordered = /^\s*\d+\.\s+/.test(line);
      const items: string[] = [];
      while (index < lines.length && /^\s*([-*+]|\d+\.)\s+/.test(at(index))) {
        items.push(at(index).replace(/^\s*([-*+]|\d+\.)\s+/, ""));
        index += 1;
      }
      const ListTag = ordered ? "ol" : "ul";
      blocks.push(
        <ListTag
          key={key++}
          className={cn("space-y-1 pl-5", ordered ? "list-decimal" : "list-disc")}
        >
          {items.map((item, itemIndex) => (
            <li key={itemIndex}>{renderInline(item)}</li>
          ))}
        </ListTag>,
      );
      continue;
    }
    if (/^\s*>\s?/.test(line)) {
      const quoted: string[] = [];
      while (index < lines.length && /^\s*>\s?/.test(at(index))) {
        quoted.push(at(index).replace(/^\s*>\s?/, ""));
        index += 1;
      }
      blocks.push(
        <blockquote
          key={key++}
          className="border-border text-muted-foreground border-l-2 pl-3 italic"
        >
          {renderInline(quoted.join(" "))}
        </blockquote>,
      );
      continue;
    }
    const paragraph: string[] = [];
    while (
      index < lines.length &&
      at(index).trim() !== "" &&
      !at(index).trimStart().startsWith("```") &&
      !/^\s*([-*+]|\d+\.)\s+/.test(at(index)) &&
      !/^\s*>\s?/.test(at(index)) &&
      !/^#{1,6}\s+/.test(at(index)) &&
      !isTableStart(at, index)
    ) {
      paragraph.push(at(index));
      index += 1;
    }
    blocks.push(<p key={key++}>{renderInline(paragraph.join(" "))}</p>);
  }
  return blocks;
}

/**
 * A fenced block, with the one action anyone wants from one.
 *
 * A model answering about a cluster answers in commands and manifests, and the
 * next thing that happens to them is a paste into a shell. Selecting one out of
 * a scrolling block with the pointer drops the last line as often as not, so the
 * block copies itself — hover-revealed, because the code is what is being read.
 */
function CodeBlock({ language, code }: { language: string; code: string }) {
  return (
    <div className="group relative">
      <pre className="border-border bg-surface-muted rounded-control overflow-x-auto border p-3 font-mono text-xs">
        {language ? (
          <span className="text-subtle-foreground mb-1 block font-sans text-[11px]">
            {language}
          </span>
        ) : null}
        <code>{code}</code>
      </pre>
      <div className="hoverless:opacity-100 absolute top-1 right-1 opacity-0 transition-opacity duration-150 group-focus-within:opacity-100 group-hover:opacity-100">
        <CopyIconButton value={code} label="复制代码" className="bg-surface/80 backdrop-blur-sm" />
      </div>
    </div>
  );
}

type Alignment = "left" | "center" | "right";

// A table is a header row plus the delimiter row under it. The delimiter is
// what tells a table apart from a paragraph that happens to contain a pipe,
// which is common in cluster output — a container command, a log line.
function isTableStart(at: (position: number) => string, index: number): boolean {
  return at(index).includes("|") && isTableDelimiter(at(index + 1));
}

function isTableDelimiter(line: string): boolean {
  const trimmed = line.trim();
  // The pipe is required, not optional: without it a bare `---` under a line of
  // prose would start a table, and a rule under a paragraph is the more likely
  // thing a model meant.
  if (!trimmed.includes("|")) return false;
  const cells = splitRow(trimmed);
  return cells.length > 0 && cells.every((cell) => /^:?-{1,}:?$/.test(cell.trim()));
}

function alignmentsOf(delimiter: string): Alignment[] {
  return splitRow(delimiter).map((cell) => {
    const value = cell.trim();
    if (value.startsWith(":") && value.endsWith(":")) return "center";
    if (value.endsWith(":")) return "right";
    return "left";
  });
}

// The outer pipes of `| a | b |` are a frame, not empty cells, so they are
// dropped — but only when they are actually there, because a row written
// without them is just as valid.
function splitRow(line: string): string[] {
  let value = line.trim();
  if (value.startsWith("|")) value = value.slice(1);
  if (value.endsWith("|")) value = value.slice(0, -1);
  return value.split("|");
}

/**
 * A model answer's table, rendered as one.
 *
 * Wide tables scroll inside their own container rather than widening the
 * conversation: the column an answer is read in is the same width whatever the
 * model chose to tabulate.
 */
function renderTable(key: number, rows: string[], alignments: Alignment[]): ReactNode {
  const header = splitRow(rows[0] ?? "");
  const body = rows.slice(1).map((row) => splitRow(row));
  const align = (column: number) =>
    ({ left: "text-left", center: "text-center", right: "text-right" })[
      alignments[column] ?? "left"
    ];
  return (
    <div key={key} className="border-border rounded-panel overflow-x-auto border">
      <table className="w-full border-collapse text-[13px]">
        <thead className="bg-surface-muted">
          <tr>
            {header.map((cell, column) => (
              <th
                key={column}
                className={cn(
                  "text-muted-foreground border-border border-b px-3 py-1.5 font-medium whitespace-nowrap",
                  align(column),
                )}
              >
                {renderInline(cell.trim())}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {body.map((cells, row) => (
            <tr key={row} className="border-border [&:not(:last-child)]:border-b">
              {header.map((_column, column) => (
                <td key={column} className={cn("px-3 py-1.5 align-top", align(column))}>
                  {renderInline((cells[column] ?? "").trim())}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Inline code first, then emphasis: a backtick span is literal by definition,
// and running emphasis over it would let `**` inside a code fragment change how
// the fragment renders.
const INLINE = /(`[^`]+`|\*\*[^*]+\*\*|__[^_]+__|\*[^*\n]+\*)/g;

function renderInline(text: string): ReactNode {
  const parts = text.split(INLINE).filter((part) => part !== "");
  return parts.map((part, index) => {
    if (part.startsWith("`") && part.endsWith("`") && part.length > 1) {
      return (
        <code
          key={index}
          className="border-border bg-surface-muted rounded-inline border px-1 py-px font-mono text-xs"
        >
          {part.slice(1, -1)}
        </code>
      );
    }
    if (
      (part.startsWith("**") && part.endsWith("**")) ||
      (part.startsWith("__") && part.endsWith("__"))
    ) {
      return (
        <strong key={index} className="font-semibold">
          {part.slice(2, -2)}
        </strong>
      );
    }
    if (part.startsWith("*") && part.endsWith("*") && part.length > 2) {
      return <em key={index}>{part.slice(1, -1)}</em>;
    }
    return <Fragment key={index}>{part}</Fragment>;
  });
}
