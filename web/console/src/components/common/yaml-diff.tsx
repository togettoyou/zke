import { Badge } from "@/components/ui/badge";

type DiffKind = "same" | "add" | "remove" | "gap";
type DiffRow = { kind: DiffKind; text: string; before?: number; after?: number };

const MAX_EXACT_CELLS = 2_000_000;
const MAX_RENDERED_ROWS = 5_000;
const MAX_ANALYZED_LINES = 20_000;
const CONTEXT_LINES = 3;

/**
 * Keeps only the unchanged lines that explain a change.
 *
 * Marking around every changed row also merges nearby changes naturally: when
 * their context overlaps there is no gap to insert between them.
 */
function collapseUnchangedRows(rows: DiffRow[]): DiffRow[] {
  const keep = new Uint8Array(rows.length);
  for (let index = 0; index < rows.length; index += 1) {
    if (rows[index]?.kind === "same") {
      continue;
    }
    const start = Math.max(0, index - CONTEXT_LINES);
    const end = Math.min(rows.length, index + CONTEXT_LINES + 1);
    keep.fill(1, start, end);
  }

  const visible: DiffRow[] = [];
  let index = 0;
  while (index < rows.length) {
    if (keep[index]) {
      visible.push(rows[index]!);
      index += 1;
      continue;
    }
    const start = index;
    while (index < rows.length && !keep[index]) {
      index += 1;
    }
    visible.push({ kind: "gap", text: `省略 ${index - start} 行未变化内容` });
  }
  return visible;
}

function buildLargeRows(left: string[], right: string[], prefix: number, suffix: number) {
  const rows: DiffRow[] = [];
  const addRange = (
    kind: Exclude<DiffKind, "gap">,
    values: string[],
    start: number,
    end: number,
  ) => {
    for (let index = start; index < end; index += 1) {
      rows.push({
        kind,
        text: values[index]!,
        ...(kind !== "add" ? { before: index + 1 } : {}),
        ...(kind !== "remove" ? { after: index + 1 } : {}),
      });
    }
  };
  const addSample = (kind: "add" | "remove", values: string[], start: number, end: number) => {
    const sampleSize = 100;
    const firstEnd = Math.min(end, start + sampleSize);
    addRange(kind, values, start, firstEnd);
    if (end > firstEnd + sampleSize) {
      rows.push({ kind: "gap", text: `省略 ${end - firstEnd - sampleSize} 行` });
      addRange(kind, values, end - sampleSize, end);
    } else {
      addRange(kind, values, firstEnd, end);
    }
  };

  const leadingContextStart = Math.max(0, prefix - CONTEXT_LINES);
  if (leadingContextStart > 0) {
    rows.push({ kind: "gap", text: `省略前方 ${leadingContextStart} 行未变化内容` });
  }
  addRange("same", left, leadingContextStart, prefix);
  addSample("remove", left, prefix, left.length - suffix);
  addSample("add", right, prefix, right.length - suffix);
  if (suffix > 0) {
    const suffixSample = Math.min(suffix, CONTEXT_LINES);
    for (let offset = 0; offset < suffixSample; offset += 1) {
      rows.push({
        kind: "same",
        text: right[right.length - suffix + offset]!,
        before: left.length - suffix + offset + 1,
        after: right.length - suffix + offset + 1,
      });
    }
    if (suffix > CONTEXT_LINES) {
      rows.push({
        kind: "gap",
        text: `省略后方 ${suffix - CONTEXT_LINES} 行未变化内容`,
      });
    }
  }
  return {
    rows,
    added: right.length - prefix - suffix,
    removed: left.length - prefix - suffix,
    truncated: true,
    coarse: true,
  };
}

function buildRows(
  before: string,
  after: string,
): {
  rows: DiffRow[];
  added: number;
  removed: number;
  truncated: boolean;
  coarse: boolean;
} {
  const left = before.split("\n");
  const right = after.split("\n");
  let prefix = 0;
  while (prefix < left.length && prefix < right.length && left[prefix] === right[prefix])
    prefix += 1;
  let suffix = 0;
  while (
    suffix < left.length - prefix &&
    suffix < right.length - prefix &&
    left[left.length - suffix - 1] === right[right.length - suffix - 1]
  ) {
    suffix += 1;
  }
  if (left.length + right.length > MAX_ANALYZED_LINES) {
    return buildLargeRows(left, right, prefix, suffix);
  }
  const leftMiddle = left.slice(prefix, left.length - suffix);
  const rightMiddle = right.slice(prefix, right.length - suffix);
  const operations: { kind: Exclude<DiffKind, "gap">; text: string }[] = left
    .slice(0, prefix)
    .map((text) => ({ kind: "same", text }));

  if (leftMiddle.length * rightMiddle.length <= MAX_EXACT_CELLS) {
    const width = rightMiddle.length + 1;
    const lengths = new Uint32Array((leftMiddle.length + 1) * width);
    for (let leftIndex = leftMiddle.length - 1; leftIndex >= 0; leftIndex -= 1) {
      for (let rightIndex = rightMiddle.length - 1; rightIndex >= 0; rightIndex -= 1) {
        const index = leftIndex * width + rightIndex;
        lengths[index] =
          leftMiddle[leftIndex] === rightMiddle[rightIndex]
            ? (lengths[(leftIndex + 1) * width + rightIndex + 1] ?? 0) + 1
            : Math.max(lengths[(leftIndex + 1) * width + rightIndex] ?? 0, lengths[index + 1] ?? 0);
      }
    }
    let leftIndex = 0;
    let rightIndex = 0;
    while (leftIndex < leftMiddle.length || rightIndex < rightMiddle.length) {
      if (
        leftIndex < leftMiddle.length &&
        rightIndex < rightMiddle.length &&
        leftMiddle[leftIndex] === rightMiddle[rightIndex]
      ) {
        operations.push({ kind: "same", text: leftMiddle[leftIndex]! });
        leftIndex += 1;
        rightIndex += 1;
      } else if (
        rightIndex < rightMiddle.length &&
        (leftIndex === leftMiddle.length ||
          (lengths[leftIndex * width + rightIndex + 1] ?? 0) >=
            (lengths[(leftIndex + 1) * width + rightIndex] ?? 0))
      ) {
        operations.push({ kind: "add", text: rightMiddle[rightIndex]! });
        rightIndex += 1;
      } else {
        operations.push({ kind: "remove", text: leftMiddle[leftIndex]! });
        leftIndex += 1;
      }
    }
  } else {
    operations.push(...leftMiddle.map((text) => ({ kind: "remove" as const, text })));
    operations.push(...rightMiddle.map((text) => ({ kind: "add" as const, text })));
  }
  operations.push(
    ...left.slice(left.length - suffix).map((text) => ({ kind: "same" as const, text })),
  );

  let beforeLine = 1;
  let afterLine = 1;
  const allRows = operations.map<DiffRow>((operation) => {
    const row: DiffRow = {
      kind: operation.kind,
      text: operation.text,
      ...(operation.kind !== "add" ? { before: beforeLine++ } : {}),
      ...(operation.kind !== "remove" ? { after: afterLine++ } : {}),
    };
    return row;
  });
  const added = allRows.filter((row) => row.kind === "add").length;
  const removed = allRows.filter((row) => row.kind === "remove").length;
  const visibleRows = collapseUnchangedRows(allRows);
  if (visibleRows.length <= MAX_RENDERED_ROWS)
    return { rows: visibleRows, added, removed, truncated: false, coarse: false };
  const half = Math.floor(MAX_RENDERED_ROWS / 2);
  return {
    rows: [
      ...visibleRows.slice(0, half),
      { kind: "gap", text: `省略 ${visibleRows.length - half * 2} 行差异内容` },
      ...visibleRows.slice(-half),
    ],
    added,
    removed,
    truncated: true,
    coarse: false,
  };
}

/** A bounded, line-oriented review of the object Kubernetes says it would store. */
export function YamlDiff({ before, after }: { before: string; after: string }) {
  const diff = buildRows(before, after);
  return (
    <section>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h4 className="text-foreground mr-auto text-sm font-medium">DryRun 最终差异</h4>
        <span className="text-subtle-foreground text-xs">变更上下各 {CONTEXT_LINES} 行</span>
        <Badge tone={diff.added > 0 ? "success" : "neutral"}>+{diff.added}</Badge>
        <Badge tone={diff.removed > 0 ? "danger" : "neutral"}>−{diff.removed}</Badge>
      </div>
      {diff.added === 0 && diff.removed === 0 ? (
        <div className="border-border bg-surface-muted rounded-panel border p-3 text-sm">
          DryRun 返回对象与当前对象没有文本差异。
        </div>
      ) : (
        <div className="border-border rounded-panel bg-surface-muted max-h-[min(28vh,320px)] overflow-auto border">
          <table className="zke-mono w-full border-collapse text-xs leading-5">
            <tbody>
              {diff.rows.map((row, index) => (
                <tr
                  key={`${index}-${row.kind}`}
                  className={
                    row.kind === "add"
                      ? "bg-success/10"
                      : row.kind === "remove"
                        ? "bg-danger/10"
                        : row.kind === "gap"
                          ? "bg-warning/10"
                          : undefined
                  }
                >
                  <td className="text-subtle-foreground border-border w-12 border-r px-2 text-right align-top select-none">
                    {row.before ?? ""}
                  </td>
                  <td className="text-subtle-foreground border-border w-12 border-r px-2 text-right align-top select-none">
                    {row.after ?? ""}
                  </td>
                  <td className="w-5 px-1 text-center align-top select-none">
                    {row.kind === "add"
                      ? "+"
                      : row.kind === "remove"
                        ? "−"
                        : row.kind === "gap"
                          ? "…"
                          : " "}
                  </td>
                  <td className="text-foreground min-w-[40rem] px-2 whitespace-pre">{row.text}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {diff.truncated ? (
        <p className="text-warning mt-2 text-xs">
          {diff.coarse
            ? "文档行数过多，已按变化区间进行粗粒度统计，并仅显示差异样本。"
            : "差异过大，仅显示开头和结尾；增删行统计仍覆盖完整文档。"}
        </p>
      ) : null}
    </section>
  );
}
