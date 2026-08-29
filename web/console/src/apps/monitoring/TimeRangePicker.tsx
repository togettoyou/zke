import { useId, useState } from "react";
import { Check, ChevronDown, Clock } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/cn";

import {
  MAX_RANGE_SECONDS,
  MIN_RANGE_SECONDS,
  RELATIVE_PRESETS,
  formatDuration,
  formatRange,
  fromLocalInput,
  toLocalInput,
  type TimeRange,
} from "./time-range";

/**
 * The control every chart answers to.
 *
 * Presets as rows rather than a calendar: "最近 1 小时" is what an operator
 * reaches for, and making them fight a date grid for it would be the wrong
 * default for the common case. The absolute range sits below a hairline for the
 * case the presets cannot express — an incident that happened last Tuesday, or
 * the window somebody sent them in a ticket.
 */
export function TimeRangePicker({
  range,
  onChange,
}: {
  range: TimeRange;
  onChange: (range: TimeRange) => void;
}) {
  const [open, setOpen] = useState(false);
  const startId = useId();
  const endId = useId();
  const [start, setStart] = useState(() => toLocalInput(Date.now() - 60 * 60 * 1000));
  const [end, setEnd] = useState(() => toLocalInput(Date.now()));

  // The custom fields open on whatever is currently being shown, so an operator
  // adjusting a window by ten minutes starts from that window rather than from
  // a default they then have to retype. Seeded when the popover opens rather
  // than in an effect: the fields are the operator's draft afterwards, and a
  // render-driven sync would type over them.
  const seedFields = () => {
    if (range.kind === "absolute") {
      setStart(toLocalInput(range.startMs));
      setEnd(toLocalInput(range.endMs));
      return;
    }
    const now = Date.now();
    setStart(toLocalInput(now - range.seconds * 1000));
    setEnd(toLocalInput(now));
  };

  const startMs = fromLocalInput(start);
  const endMs = fromLocalInput(end);
  const problem = describeProblem(startMs, endMs);

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        if (next) {
          seedFields();
        }
        setOpen(next);
      }}
    >
      <PopoverTrigger asChild>
        <Button size="sm" variant="secondary" aria-label={`时间范围：${formatRange(range)}`}>
          <Clock aria-hidden />
          {formatRange(range)}
          <ChevronDown className="text-subtle-foreground" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[19rem] p-0">
        <div className="flex flex-col p-1.5">
          {RELATIVE_PRESETS.map((preset) => {
            const active = range.kind === "relative" && range.seconds === preset.seconds;
            return (
              <button
                key={preset.seconds}
                type="button"
                onClick={() => {
                  onChange({ kind: "relative", seconds: preset.seconds });
                  setOpen(false);
                }}
                className={cn(
                  "zke-focus rounded-control flex items-center justify-between gap-2 px-2 py-1.5 text-left text-[13px] transition-colors",
                  active
                    ? "text-foreground font-medium"
                    : "text-muted-foreground hover:bg-surface-muted hover:text-foreground",
                )}
              >
                {preset.label}
                {active ? <Check className="text-primary size-4" aria-hidden /> : null}
              </button>
            );
          })}
        </div>
        <div className="border-border flex flex-col gap-2 border-t p-3">
          <p className="text-subtle-foreground text-[11px] font-medium">自定义范围</p>
          <div className="flex flex-col gap-1">
            <label htmlFor={startId} className="text-muted-foreground text-xs">
              开始
            </label>
            <Input
              id={startId}
              type="datetime-local"
              value={start}
              className="h-8 text-[13px]"
              onChange={(event) => setStart(event.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor={endId} className="text-muted-foreground text-xs">
              结束
            </label>
            <Input
              id={endId}
              type="datetime-local"
              value={end}
              className="h-8 text-[13px]"
              onChange={(event) => setEnd(event.target.value)}
            />
          </div>
          {problem ? <p className="text-danger text-xs">{problem}</p> : null}
          <Button
            size="sm"
            variant="primary"
            disabled={Boolean(problem)}
            onClick={() => {
              if (startMs === null || endMs === null || problem) {
                return;
              }
              onChange({ kind: "absolute", startMs, endMs });
              setOpen(false);
            }}
          >
            应用
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

/**
 * The Server refuses a range wider than its own limit and a step below it, so
 * the refusal is spelled out here instead of arriving as a failed request the
 * operator has to translate back into "that window was too wide".
 */
function describeProblem(startMs: number | null, endMs: number | null): string | null {
  if (startMs === null || endMs === null) {
    return "请填写完整的开始与结束时间。";
  }
  const seconds = Math.round((endMs - startMs) / 1000);
  if (seconds <= 0) {
    return "结束时间必须晚于开始时间。";
  }
  if (seconds < MIN_RANGE_SECONDS) {
    return `范围不能短于 ${formatDuration(MIN_RANGE_SECONDS)}。`;
  }
  if (seconds > MAX_RANGE_SECONDS) {
    return `范围不能超过 ${formatDuration(MAX_RANGE_SECONDS)}。`;
  }
  return null;
}
