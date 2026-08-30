import { useMemo, useState } from "react";
import { BarChart3, Braces, Table2 } from "lucide-react";

import type { MetricsExploreOutcome } from "@/api/types";
import { CopyIconButton } from "@/components/common/copy";
import { EmptyState, LoadingState } from "@/components/common/state";
import { Alert } from "@/components/ui/misc";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { HintTooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";
import { highlightJson, type JsonTokenKind } from "@/lib/json-highlight";

import { ChartLegend } from "../ChartLegend";
import { IssueNotice } from "../ChartPanel";
import { TimeSeriesChart } from "../TimeSeriesChart";
import { seriesColor, seriesDash, useChartPalette } from "../chart-palette";
import { useMetricsScope } from "../metrics-scope";
import { useExplore } from "./explore-context";
import {
  chartTimestamps,
  exploreSeriesRows,
  formatExploreAxis,
  formatExploreValue,
  toLegendSeries,
  type ExploreSeriesRow,
} from "./explore-series";

/** Every chart in the monitoring application shares a crosshair. */
const SYNC_KEY = "zke-monitoring";

/** Beyond this a table scrolls inside itself rather than pushing the page down. */
const TABLE_MAX_HEIGHT = "26rem";

/**
 * Taller than a catalogue panel's 200.
 *
 * Those are one of six charts tiled two to a row; this is the whole answer on a
 * screen that has nothing else competing for the space, and the extra height is
 * what makes several expressions on shared axes readable rather than a knot.
 */
const CHART_HEIGHT = 320;

const TOKEN_CLASS: Record<JsonTokenKind, string | undefined> = {
  plain: undefined,
  key: "text-code-key",
  string: "text-code-string",
  literal: "text-code-literal",
  punctuation: "text-code-punctuation",
};

/**
 * The answer, in the three shapes an operator reads it in.
 *
 * A curve answers "when did it change", a table answers "which one is highest"
 * and the raw document answers "what exactly came back" — the last of which is
 * what somebody debugging a recording rule or an alert actually needs. They are
 * tabs rather than three panels because they are the same answer, and a screen
 * showing all three at once would put the chart above the fold on nobody's
 * monitor.
 */
export function ExploreResults() {
  const { result, runnable, running, untouched } = useExplore();
  const { selectRange } = useMetricsScope();
  const palette = useChartPalette();
  const [tab, setTab] = useState("chart");
  const [hidden, setHidden] = useState<ReadonlySet<string>>(() => new Set());

  const succeeded = useMemo(
    () => (result?.queries ?? []).filter((outcome) => !outcome.error),
    [result],
  );
  const timestamps = useMemo(() => chartTimestamps(succeeded), [succeeded]);
  const rows = useMemo(() => exploreSeriesRows(succeeded, timestamps), [succeeded, timestamps]);
  const legend = useMemo(() => toLegendSeries(rows), [rows]);
  const truncated = succeeded.some((outcome) => outcome.truncated);

  // Read from the answer rather than from the editor's current setting: the
  // toggle can be flipped without re-running, and the shape on screen is still
  // the shape of what came back.
  //
  // An instant query has one point per series, so a curve would be a single dot
  // per line. The table is the shape that answer comes in, and the tab moves
  // rather than showing an empty plot.
  const instant = result?.kind === "instant";
  const chartable = result?.kind === "range" && timestamps.length > 1;
  const active = !chartable && tab === "chart" ? "table" : tab;

  const toggle = (id: string) =>
    setHidden((current) => {
      const next = new Set(current);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });

  if (untouched) {
    return (
      <section className="border-border bg-surface rounded-panel border p-4">
        <EmptyState
          title="写一条表达式，然后执行"
          description="自定义查询针对上方工具栏选定的目标集群执行。表达式中的集群条件会由 Server 强制替换为该集群，因此可以放心地粘贴任何来源的表达式。"
        />
      </section>
    );
  }

  // Everything was hidden or deleted, and the run that noticed dropped the
  // answer with it. Both conditions are needed: while somebody is midway
  // through clearing a box the answer is still on screen and still correct for
  // what was last asked, and blanking the panel on a keystroke would be worse
  // than the 表达式已修改 warning that covers it.
  if (!runnable && !result && !running) {
    return (
      <section className="border-border bg-surface rounded-panel border p-4">
        <EmptyState
          title="没有要执行的表达式"
          description="全部表达式都已隐藏或删除。启用其中一条，或者写一条新的。"
        />
      </section>
    );
  }

  return (
    <section className="border-border bg-surface rounded-panel flex min-w-0 flex-col border p-4">
      <Tabs value={active} onValueChange={setTab} className="min-w-0">
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
          <TabsList>
            {/* The disabled tab keeps its tooltip on a wrapping span: a
                disabled control fires no pointer events, so Radix has nothing
                to hang the explanation on. */}
            <HintTooltip
              label={chartable ? "按时间绘制曲线" : "瞬时查询每条序列只有一个采样点，没有曲线可画"}
            >
              <span>
                <TabsTrigger value="chart" disabled={!chartable}>
                  <BarChart3 className="size-3.5" aria-hidden />
                  图表
                </TabsTrigger>
              </span>
            </HintTooltip>
            <TabsTrigger value="table">
              <Table2 className="size-3.5" aria-hidden />
              表格
            </TabsTrigger>
            <TabsTrigger value="json">
              <Braces className="size-3.5" aria-hidden />
              JSON
            </TabsTrigger>
          </TabsList>
          <ResultSummary
            running={running}
            instant={instant}
            series={rows.length}
            expressions={succeeded.length}
          />
        </div>

        <TabsContent value="chart">
          {rows.length === 0 ? (
            <NoSeries running={running} />
          ) : (
            <div className="min-w-0">
              <TimeSeriesChart
                timestamps={timestamps}
                series={legend}
                palette={palette}
                formatValue={formatExploreValue}
                formatAxis={formatExploreAxis}
                ariaLabel="自定义查询结果"
                height={CHART_HEIGHT}
                hidden={hidden}
                onSelectRange={selectRange}
                syncKey={SYNC_KEY}
              />
              {/* Wider number columns than a catalogue panel's: those values
                  carry a unit that shortens them, and these are raw. */}
              <ChartLegend
                series={legend}
                hidden={hidden}
                onToggle={toggle}
                formatValue={formatExploreValue}
                colorAt={(index) => seriesColor(palette, index)}
                dashAt={seriesDash}
                numberColumnWidth="7rem"
              />
            </div>
          )}
        </TabsContent>

        <TabsContent value="table">
          {rows.length === 0 ? (
            <NoSeries running={running} />
          ) : (
            <div className="flex min-w-0 flex-col gap-4">
              {succeeded.map((outcome) => (
                <OutcomeTable
                  key={outcome.ref_id}
                  outcome={outcome}
                  instant={instant}
                  rows={rows.filter((row) => row.reference === outcome.ref_id)}
                />
              ))}
            </div>
          )}
        </TabsContent>

        <TabsContent value="json">
          <RawDocument value={result} />
        </TabsContent>
      </Tabs>

      {truncated ? (
        <Alert tone="warning" className="mt-3">
          序列数超过上限，图表与表格只显示了前若干条。请在表达式中聚合或收窄标签选择器。
        </Alert>
      ) : null}
      <IssueNotice issues={result?.issues ?? []} />
    </section>
  );
}

/**
 * What the answer covers, at the right end of the tab row.
 *
 * It names the shape as well as the size: 瞬时 and 区间 produce tables that look
 * alike and mean different things, and saying which is cheaper for the reader
 * than working it out from the column headings.
 */
function ResultSummary({
  running,
  instant,
  series,
  expressions,
}: {
  running: boolean;
  instant: boolean;
  series: number;
  expressions: number;
}) {
  return (
    <p className="text-subtle-foreground flex items-center gap-2 text-[11px]">
      {/* Always mounted and faded rather than conditionally rendered:
          appearing and disappearing reflows the row it shares with the tabs. */}
      <span
        aria-hidden={!running}
        className={cn("transition-opacity duration-200", running ? "opacity-100" : "opacity-0")}
      >
        执行中…
      </span>
      <span className="zke-tnum">
        {instant ? "瞬时" : "区间"} · {series} 条序列 · {expressions} 条表达式
      </span>
    </p>
  );
}

function NoSeries({ running }: { running: boolean }) {
  if (running) {
    return <LoadingState />;
  }
  return (
    <EmptyState
      title="没有匹配的序列"
      description="表达式执行成功，但在该时间范围内没有匹配到任何序列。请确认指标名称、标签条件，以及该集群是否已经安装对应的采集组件。"
    />
  );
}

/**
 * One expression's series as a table.
 *
 * The label columns are the union of the labels its own series carry, so an
 * aggregation that grouped by node gets a `node` column and nothing else. The
 * alternative — one fixed column of rendered labels — is what makes a table of
 * forty Pods unreadable and unsortable at once.
 */
function OutcomeTable({
  outcome,
  instant,
  rows,
}: {
  outcome: MetricsExploreOutcome;
  instant: boolean;
  rows: ExploreSeriesRow[];
}) {
  const columns = useMemo(() => {
    const names = new Set<string>();
    for (const row of rows) {
      for (const name of Object.keys(row.labels)) {
        names.add(name);
      }
    }
    // `__name__` first: it is the metric, and the rest describe it.
    const rest = [...names].filter((name) => name !== "__name__").sort();
    return names.has("__name__") ? ["__name__", ...rest] : rest;
  }, [rows]);

  // An instant answer has one sample per series, so min, median and max would
  // be three copies of the same number. Only the value is worth a column.
  const valueColumns = instant
    ? ([{ key: "last", label: "值" }] as const)
    : ([
        { key: "min", label: "最小" },
        { key: "median", label: "中位" },
        { key: "max", label: "最大" },
        { key: "last", label: "最新" },
      ] as const);

  return (
    <div className="min-w-0">
      <div className="mb-1.5 flex min-w-0 items-baseline gap-2">
        <span className="text-foreground shrink-0 text-xs font-medium">
          表达式 {outcome.ref_id}
        </span>
        {/* Truncated with the full text on hover: an aggregation over three
            rate windows is longer than any window is wide, and a heading that
            wraps to four lines pushes the table it labels off the screen. */}
        <span
          className="zke-mono text-muted-foreground min-w-0 truncate text-[11px]"
          title={outcome.expression}
        >
          {outcome.expression}
        </span>
      </div>
      {rows.length === 0 ? (
        <p className="text-subtle-foreground border-border rounded-panel border px-3 py-2 text-xs">
          该表达式没有匹配到序列。
        </p>
      ) : (
        // Its own scroller in both directions: a query grouped by five labels
        // is wider than any window, and letting it push the page sideways
        // would take the toolbar with it.
        <div
          className="border-border rounded-panel overflow-auto border"
          style={{ maxHeight: TABLE_MAX_HEIGHT }}
        >
          <table className="w-full min-w-max border-collapse text-xs">
            <thead>
              {/* Sticky, because the numbers below stop meaning anything once
                  the heading that names them has scrolled away. */}
              <tr className="bg-surface-muted sticky top-0 z-10">
                {columns.map((name) => (
                  <th
                    key={name}
                    scope="col"
                    className="border-border text-subtle-foreground zke-mono border-b px-3 py-2 text-left font-normal"
                  >
                    {name}
                  </th>
                ))}
                {valueColumns.map((column) => (
                  <th
                    key={column.key}
                    scope="col"
                    className="border-border text-subtle-foreground border-b px-3 py-2 text-right font-normal"
                  >
                    {column.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={row.id}
                  className="border-border hover:bg-surface-muted border-b transition-colors last:border-b-0"
                >
                  {columns.map((name) => (
                    <td
                      key={name}
                      className="text-muted-foreground zke-mono max-w-[22rem] truncate px-3 py-1.5"
                      title={row.labels[name]}
                    >
                      {row.labels[name] ?? "—"}
                    </td>
                  ))}
                  {valueColumns.map((column) => (
                    <NumberCell
                      key={column.key}
                      value={valueOf(row, column.key)}
                      emphasis={column.key === "last"}
                    />
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function valueOf(row: ExploreSeriesRow, key: "min" | "median" | "max" | "last"): number | null {
  switch (key) {
    case "min":
      return row.minimum;
    case "median":
      return row.median;
    case "max":
      return row.stats.max;
    default:
      return row.stats.last;
  }
}

function NumberCell({ value, emphasis }: { value: number | null; emphasis?: boolean }) {
  return (
    <td
      className={cn(
        "zke-tnum px-3 py-1.5 text-right whitespace-nowrap",
        emphasis ? "text-foreground font-medium" : "text-muted-foreground",
      )}
    >
      {value === null ? "—" : formatExploreValue(value)}
    </td>
  );
}

/**
 * The answer exactly as it arrived.
 *
 * This is the tab somebody opens when a chart disagrees with what they expected
 * and they need to see the labels and the raw values — so it shows the whole
 * document, including the rewritten expression the Server actually ran, rather
 * than a tidied projection of it.
 *
 * Coloured with the same `--code-*` tokens the YAML editor uses, so a document
 * does not change palette depending on which viewer it is open in. The colour
 * is what turns a screen of grey monospace into something a key can be found
 * in.
 */
function RawDocument({ value }: { value: unknown }) {
  const text = useMemo(() => JSON.stringify(value, null, 2) ?? "", [value]);
  const tokens = useMemo(() => highlightJson(text), [text]);
  return (
    <div className="relative min-w-0">
      {/* Outside the scroller, so it stays reachable at the bottom of a long
          document. The padding on the right keeps it off the first line. */}
      <div className="absolute top-2 right-2 z-10">
        <CopyIconButton value={text} label="复制 JSON" />
      </div>
      <pre className="zke-mono border-border bg-surface-muted rounded-panel max-h-[32rem] overflow-auto border p-3 pr-12 text-[11px] leading-relaxed">
        {tokens.map((token, index) => (
          <span key={index} className={TOKEN_CLASS[token.kind]}>
            {token.text}
          </span>
        ))}
      </pre>
    </div>
  );
}
