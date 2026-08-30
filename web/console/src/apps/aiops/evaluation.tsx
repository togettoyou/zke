import { useAIEvaluation } from "@/api/queries/aiops";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";

export function Evaluation({
  tenantId,
  projectId,
  clusterId,
}: {
  tenantId: string;
  projectId: string;
  clusterId: string;
}) {
  const query = useAIEvaluation(tenantId, projectId, clusterId);
  if (query.isPending) return <LoadingState label="统计诊断效果" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;

  const evaluation = query.data;
  if (!evaluation || evaluation.turns === 0) {
    return (
      <EmptyState
        title="暂无可评估的运行"
        description="完成一次 AIOps 诊断后，这里会汇总最近 30 天的运行结果与用户反馈。"
      />
    );
  }

  return (
    <div className="min-h-0 flex-1 overflow-auto px-5 py-4">
      <div className="mx-auto max-w-4xl space-y-4">
        <div>
          <h3 className="text-foreground text-sm font-semibold">最近 30 天诊断效果</h3>
          <p className="text-subtle-foreground mt-1 text-xs">
            仅统计你在当前 Cluster 的运行与反馈；帮助率和解决率以已反馈 Turn 为分母。
          </p>
        </div>

        <div className="grid grid-cols-2 gap-3 @2xl:grid-cols-4">
          <Metric
            label="完成率"
            value={ratio(evaluation.succeeded, evaluation.turns)}
            detail={`${evaluation.succeeded}/${evaluation.turns} Turn`}
          />
          <Metric
            label="反馈覆盖率"
            value={ratio(evaluation.rated, evaluation.succeeded)}
            detail={`${evaluation.rated}/${evaluation.succeeded} 个成功 Turn`}
          />
          <Metric
            label="帮助率"
            value={ratio(evaluation.helpful, evaluation.rated)}
            detail={`${evaluation.helpful}/${evaluation.rated} 份反馈`}
          />
          <Metric
            label="问题解决率"
            value={ratio(evaluation.resolved, evaluation.rated)}
            detail={`${evaluation.resolved}/${evaluation.rated} 份反馈`}
          />
        </div>

        <div className="border-border bg-surface rounded-panel grid grid-cols-2 gap-4 border p-4 @md:grid-cols-4">
          <Datum label="失败" value={evaluation.failed} />
          <Datum label="取消" value={evaluation.canceled} />
          <Datum label="平均工具调用" value={average(evaluation.tool_calls, evaluation.turns)} />
          <Datum label="平均记录耗时" value={duration(evaluation.duration_ms, evaluation.turns)} />
        </div>

        {Object.keys(evaluation.reason_counts).length > 0 ? (
          <Breakdown title="改进原因" values={evaluation.reason_counts} labels={reasonLabels} />
        ) : null}
        {Object.keys(evaluation.failure_counts).length > 0 ? (
          <Breakdown title="运行失败分类" values={evaluation.failure_counts} labels={{}} />
        ) : null}
      </div>
    </div>
  );
}

function Metric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <div className="border-border bg-surface rounded-panel border p-3">
      <p className="text-subtle-foreground text-[11px]">{label}</p>
      <p className="text-foreground zke-tnum mt-1 text-[22px] font-semibold">{value}</p>
      <p className="text-muted-foreground mt-1 text-[11px]">{detail}</p>
    </div>
  );
}

function Datum({ label, value }: { label: string; value: string | number }) {
  return (
    <div>
      <p className="text-subtle-foreground text-[11px]">{label}</p>
      <p className="text-foreground zke-tnum mt-1 text-sm font-medium">{value}</p>
    </div>
  );
}

function Breakdown({
  title,
  values,
  labels,
}: {
  title: string;
  values: Record<string, number>;
  labels: Record<string, string>;
}) {
  return (
    <section className="border-border bg-surface rounded-panel border p-4">
      <h4 className="text-foreground text-sm font-medium">{title}</h4>
      <ul className="mt-3 space-y-2">
        {Object.entries(values)
          .sort((left, right) => right[1] - left[1])
          .map(([name, count]) => (
            <li key={name} className="flex items-center justify-between gap-3 text-[13px]">
              <span className="text-muted-foreground">{labels[name] ?? name}</span>
              <span className="text-foreground zke-tnum">{count}</span>
            </li>
          ))}
      </ul>
    </section>
  );
}

const reasonLabels: Record<string, string> = {
  inaccurate: "结论不准确",
  insufficient_evidence: "证据不足",
  incomplete: "诊断不完整",
  unsafe: "建议风险过高",
  hard_to_follow: "难以执行",
  other: "其他",
};

function ratio(value: number, total: number): string {
  return total > 0 ? `${Math.round((value / total) * 100)}%` : "—";
}

function average(value: number, total: number): string {
  return total > 0 ? (value / total).toFixed(1) : "—";
}

function duration(value: number, total: number): string {
  if (total <= 0) return "—";
  const seconds = Math.round(value / total / 1000);
  return seconds < 60 ? `${seconds} 秒` : `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
}
