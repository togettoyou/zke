import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Check, CircleDashed, LoaderCircle, X } from "lucide-react";

import { useHelmOperation } from "@/api/queries/helm";
import type { HelmReleaseOperation, HelmReleaseOperationStage } from "@/api/types";
import { ErrorAlert } from "@/components/common/error-alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Alert } from "@/components/ui/misc";
import { cn } from "@/lib/cn";

import { HELM_ACTION_LABELS } from "./labels";

/**
 * What a release change is doing, while it is doing it.
 *
 * Installing an application is the longest-running thing anyone does in this
 * Console. It downloads a chart, renders it, writes every object the
 * application owns, and then waits for all of them to become ready — minutes,
 * in the ordinary case. Before this existed the operator was shown a disabled
 * button for all of it and then, at the end, either a release or an error; a
 * slow download and a Cluster that would never answer looked exactly alike.
 *
 * So the Server keeps an account of the operation and this reads it: which part
 * of the pipeline it is in, how long it has been going, and Helm's own log as
 * the Cluster writes it. "beginning wait for 6 resources with timeout of 5m0s"
 * is the line that turns four minutes of nothing into four minutes of something
 * expected.
 */

type StepDefinition = {
  stage: HelmReleaseOperationStage;
  label: string;
  hint: string;
};

/** The stages an install or an upgrade goes through, in order. */
const CHART_STEPS: StepDefinition[] = [
  {
    stage: "resolving_chart",
    label: "解析并下载 Chart",
    hint: "读取仓库索引、确定版本、取回归档并按仓库的签名策略校验来源",
  },
  {
    stage: "validating_values",
    label: "校验 values",
    hint: "按 Chart 自带的 values.schema.json 检查；不通过则集群不会收到请求",
  },
  {
    stage: "executing",
    label: "在集群中渲染并写入",
    hint: "由目标集群的 Agent 用 Helm 自己的引擎执行，下面是它的日志",
  },
];

/** A rollback or an uninstall replays what Helm already stored; there is no chart. */
const STORED_STEPS: StepDefinition[] = [
  {
    stage: "executing",
    label: "在集群中执行",
    hint: "由目标集群的 Agent 用 Helm 自己的引擎执行，下面是它的日志",
  },
];

function operationTitle(operation: HelmReleaseOperation): string {
  const action = HELM_ACTION_LABELS[operation.action];
  return operation.dry_run ? `预览${action}` : `${action} ${operation.release_name}`;
}

/**
 * The account of one operation: where it is, how long it has taken, and what
 * the Cluster has said.
 *
 * It renders the same whether the operation is running, finished or failed —
 * the log an operator wants to read is the one from the deployment that went
 * wrong, and hiding it once the answer arrives would take it away exactly then.
 */
export function OperationProgress({
  operation,
  className,
}: {
  operation: HelmReleaseOperation;
  className?: string;
}) {
  const steps =
    operation.action === "install" || operation.action === "upgrade" ? CHART_STEPS : STORED_STEPS;
  const current = steps.findIndex((step) => step.stage === operation.stage);
  const elapsed = useElapsed(operation);

  return (
    <div className={cn("grid gap-3", className)}>
      <ol className="grid gap-2">
        {steps.map((step, index) => (
          <StepRow
            key={step.stage}
            step={step}
            state={stepState(operation, current, index)}
            /* Only the step that is actually running carries the clock. A
               finished operation showing a live timer would keep counting time
               nothing is spending. */
            elapsed={stepState(operation, current, index) === "running" ? elapsed : undefined}
          />
        ))}
      </ol>
      <OperationLog operation={operation} />
      {operation.status === "failed" && operation.failure ? (
        <Alert tone="danger">
          <span className="font-medium">{operation.failure.message}</span>
          <span className="text-danger/70 zke-mono ml-1.5 text-xs">{operation.failure.code}</span>
        </Alert>
      ) : null}
    </div>
  );
}

type StepState = "pending" | "running" | "done" | "failed";

function stepState(operation: HelmReleaseOperation, current: number, index: number): StepState {
  if (operation.status === "succeeded") {
    return "done";
  }
  // An operation that has not reported a stage yet has been accepted and
  // nothing more; its first step is what it is about to start.
  const reached = current < 0 ? 0 : current;
  if (index < reached) {
    return "done";
  }
  if (index > reached) {
    return "pending";
  }
  return operation.status === "failed" ? "failed" : "running";
}

function StepRow({
  step,
  state,
  elapsed,
}: {
  step: StepDefinition;
  state: StepState;
  elapsed?: string;
}) {
  return (
    <li className="flex items-start gap-2.5">
      <StepGlyph state={state} />
      <div className="grid min-w-0 flex-1 gap-0.5">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span
            className={cn(
              "text-[13px]",
              state === "pending" ? "text-subtle-foreground" : "text-foreground font-medium",
            )}
          >
            {step.label}
          </span>
          {elapsed ? (
            <span className="text-muted-foreground zke-tnum text-xs">{elapsed}</span>
          ) : null}
        </div>
        {state === "pending" ? null : (
          <p className="text-subtle-foreground text-xs leading-relaxed">{step.hint}</p>
        )}
      </div>
    </li>
  );
}

function StepGlyph({ state }: { state: StepState }) {
  const shell = "mt-0.5 flex size-4.5 shrink-0 items-center justify-center rounded-full border";
  switch (state) {
    case "done":
      return (
        <span className={cn(shell, "border-success/40 bg-success-surface text-success")}>
          <Check className="size-3" strokeWidth={3} aria-hidden />
        </span>
      );
    case "failed":
      return (
        <span className={cn(shell, "border-danger/40 bg-danger-surface text-danger")}>
          <X className="size-3" strokeWidth={3} aria-hidden />
        </span>
      );
    case "running":
      return (
        <span className={cn(shell, "border-primary/40 bg-primary-surface text-primary")}>
          <LoaderCircle className="size-3 animate-spin" aria-hidden />
        </span>
      );
    default:
      return (
        <span className={cn(shell, "border-border text-subtle-foreground")}>
          <CircleDashed className="size-3" aria-hidden />
        </span>
      );
  }
}

/**
 * The lines themselves.
 *
 * It follows the tail while the operator is at the bottom and stops following
 * the moment they scroll up — reading a line from thirty seconds ago is the
 * only reason to scroll in a log that is still being written, and yanking it
 * back down would make that impossible.
 */
function OperationLog({ operation }: { operation: HelmReleaseOperation }) {
  // Recomputed only when the account actually grew. A poll that brought no new
  // lines hands back the same array, so this stays the same array too, and the
  // rows below are spared a reconcile they would have nothing to show for.
  const lines = useMemo(
    () => operation.events.filter((event) => event.message !== ""),
    [operation.events],
  );
  // The rows themselves, memoised on the same array. React compares children by
  // reference before it compares them element by element, so a second in which
  // nothing was logged costs nothing here at all — which matters because a wait
  // is mostly such seconds and this list can hold five hundred rows.
  const rows = useMemo(
    () => lines.map((event) => <LogLine key={event.seq} at={event.at} message={event.message} />),
    [lines],
  );
  const box = useRef<HTMLDivElement>(null);
  const following = useRef(true);

  // Layout effect rather than an effect: the scroll has to happen in the same
  // frame the line was painted in, or the log visibly jumps.
  useLayoutEffect(() => {
    const element = box.current;
    if (element && following.current) {
      element.scrollTop = element.scrollHeight;
    }
  }, [lines.length]);

  return (
    <div
      ref={box}
      onScroll={(event) => {
        const element = event.currentTarget;
        following.current = element.scrollHeight - element.scrollTop - element.clientHeight < 24;
      }}
      className="border-border bg-surface-muted/60 rounded-control h-56 overflow-y-auto border p-2.5"
      role="log"
      aria-label="Helm 操作日志"
      aria-live="polite"
    >
      {operation.events_truncated ? (
        <p className="text-subtle-foreground mb-1 text-xs">
          日志过长，中间的一段已被丢弃；保留的是开头与结尾。
        </p>
      ) : null}
      {lines.length === 0 ? (
        <p className="text-subtle-foreground text-xs">等待第一条日志…</p>
      ) : (
        <ol className="grid gap-0.5">{rows}</ol>
      )}
    </div>
  );
}

/**
 * One line, memoised on its own text.
 *
 * A log this long is redrawn once a second for as long as the deployment runs,
 * and every line but the newest is the same line it was a second ago. Keyed on
 * the sequence number — which never repeats and never shifts, even when the
 * middle of the log is dropped — so React can tell that for itself.
 */
const LogLine = memo(function LogLine({ at, message }: { at: string; message: string }) {
  return (
    <li className="zke-mono text-muted-foreground flex gap-2 text-xs leading-relaxed">
      <span className="text-subtle-foreground zke-tnum shrink-0">{clockTime(at)}</span>
      {/* `break-all` rather than `truncate`: a Kubernetes error names the object
          it is about at the end of the line, which is exactly the half a
          truncation would take. */}
      <span className="min-w-0 break-all">{message}</span>
    </li>
  );
});

/**
 * How long the operation has been going.
 *
 * It ticks once a second while the operation runs and freezes on the total when
 * it stops. Elapsed time is the one thing that says "still working" when Helm
 * has nothing to log — during a wait it can be quiet for minutes.
 */
function useElapsed(operation: HelmReleaseOperation): string {
  const [now, setNow] = useState(() => Date.now());
  const running = operation.status === "running";
  useEffect(() => {
    if (!running) {
      return;
    }
    const timer = window.setInterval(() => setNow(Date.now()), 1_000);
    return () => window.clearInterval(timer);
  }, [running]);
  const started = Date.parse(operation.started_at);
  const ended = operation.finished_at ? Date.parse(operation.finished_at) : now;
  if (!Number.isFinite(started) || !Number.isFinite(ended)) {
    return "";
  }
  return formatElapsed(Math.max(0, Math.round((ended - started) / 1000)));
}

function formatElapsed(seconds: number): string {
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return minutes > 0 ? `${minutes} 分 ${rest} 秒` : `${rest} 秒`;
}

function clockTime(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return "--:--:--";
  }
  return parsed.toLocaleTimeString("zh-CN", { hour12: false });
}

/**
 * A rollback and an uninstall in progress.
 *
 * Both are decisions about a target rather than about content, so they are
 * confirmed in a dialog — and this is where that dialog goes once it has been
 * confirmed. It is deliberately not a page: there is nothing here to compare or
 * to scroll through, only a short account of a short operation, and sending the
 * operator to another view to read it would lose the list they acted from.
 *
 * Installing and upgrading do not use this. Their preview is a rendered
 * manifest that has to be read and compared, which is a page — see
 * ReleaseFormView.
 */
export function OperationDialog({
  clusterId,
  namespace,
  operationId,
  onClose,
}: {
  clusterId: string;
  namespace: string;
  operationId: string | null;
  onClose: () => void;
}) {
  const query = useHelmOperation(clusterId, namespace, operationId);
  const operation = query.data?.operation;
  // Unknown counts as running while the account is still loading, but not once
  // the read itself has failed: there is nothing left to wait for then.
  const running = operation ? operation.status === "running" : !query.error;

  /*
   * It closes at any time, including mid-operation.
   *
   * The operation belongs to the Server, not to this dialog: closing stops the
   * polling and nothing else, and the account — stages, elapsed time and the
   * whole log — is still there afterwards. The list keeps a banner for every
   * running operation in the namespace, so there is always a way back into it.
   * Holding the dialog open until the deployment finished only pinned the
   * operator to a wait that can last minutes and blocked the rest of the
   * Console behind it.
   */
  return (
    <Dialog open={operationId !== null} onOpenChange={(open) => open || onClose()}>
      <DialogContent
        className="w-[min(680px,calc(100vw-2rem))]"
        /* Leaving is a decision, so it takes the close button, Esc or the
           footer. A running deployment is watched for minutes, and a stray
           click on the window behind it is not someone saying they are done
           reading the log. */
        onPointerDownOutside={(event) => {
          if (running) {
            event.preventDefault();
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>{operation ? operationTitle(operation) : "正在执行"}</DialogTitle>
        </DialogHeader>
        {query.error ? (
          <ErrorAlert error={query.error} />
        ) : operation ? (
          <OperationProgress operation={operation} />
        ) : (
          <div className="text-muted-foreground flex items-center gap-2 py-6 text-[13px]">
            <LoaderCircle className="size-4 animate-spin" aria-hidden />
            正在读取操作进展…
          </div>
        )}
        <DialogFooter>
          {running ? (
            <span className="text-subtle-foreground mr-auto self-center text-xs">
              关闭不会中断它，操作会在集群里继续执行；从列表的提醒可以回到这里。
            </span>
          ) : null}
          <Button variant="secondary" onClick={onClose}>
            关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
