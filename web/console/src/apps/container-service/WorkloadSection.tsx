import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { ArrowLeft } from "lucide-react";

import { useWorkload, useWorkloads } from "@/api/queries/workloads";
import type {
  KubernetesWorkloadDetail,
  KubernetesWorkloadResource,
  KubernetesWorkloadSummary,
} from "@/api/types";
import { SectionTitle } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { DetailCard, DetailKeyValues, DetailRow } from "@/components/common/detail";
import { ErrorState, LoadingState } from "@/components/common/state";
import { StatusBadge } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatAbsolute } from "@/lib/time";

import { ContinuePager } from "./ContinuePager";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { WorkloadActions } from "./WorkloadActions";
import { kindLabel, WORKLOAD_TYPES } from "./workload-catalog";

const PAGE_SIZE = 50;

type ReplicaStatus = NonNullable<KubernetesWorkloadSummary["replicas"]>;
type JobStatus = NonNullable<KubernetesWorkloadSummary["job"]>;
type CronJobStatus = NonNullable<KubernetesWorkloadSummary["cron_job"]>;

type WorkloadSectionProps = ClusterSectionProps & {
  /** The Namespace every query and mutation in this section is scoped to. */
  namespace: string;
};

/**
 * Workloads are namespaced, so every query here is scoped by the target Cluster,
 * the Namespace picked in the toolbar and one workload type — the three path
 * segments of the endpoint. Scaling, restarting, suspending and deleting act on
 * the same three, and each goes through DryRun, an impact list and an explicit
 * confirmation before it is written.
 */
export function WorkloadSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
}: WorkloadSectionProps) {
  const { permissions } = useSessionContext();
  const [resource, setResource] = useState<KubernetesWorkloadResource>("deployments");
  // Switching Cluster, Namespace or type produces a different list, and a
  // continuation token is only meaningful to the list that issued it.
  const pager = useContinuePagination(`${clusterId}/${namespace}/${resource}`);
  const workloads = useWorkloads(clusterId, namespace, resource, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  // Drilling into a workload keeps the list's type and paging alive, so coming
  // back lands where the operator left rather than on Deployments page one.
  const [detailName, setDetailName] = useState<string | null>(null);

  const projectScope = { type: "project" as const, tenantId, projectId };
  // Scaling, restarting and suspending are all patches of the object, so they
  // are updates rather than workload-specific permissions.
  const canUpdate = permissions.can("cluster.resource.update", projectScope);
  const canDelete = permissions.can("cluster.resource.delete", projectScope);

  const columns = useMemo<ColumnDef<KubernetesWorkloadSummary, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <span className="zke-mono text-subtle-foreground text-xs">
              {row.original.uid || "UID 尚未分配"}
            </span>
          </div>
        ),
      },
      {
        header: "状态",
        size: 120,
        cell: ({ row }) => <StatusBadge kind="workload" value={row.original.status} />,
      },
      progressColumn(resource),
      {
        header: "镜像",
        cell: ({ row }) => <ImageCell images={row.original.images} />,
      },
      {
        header: "创建时间",
        size: 170,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">
            {formatAbsolute(row.original.creation_timestamp)}
          </span>
        ),
      },
      {
        id: "actions",
        header: "",
        size: 48,
        cell: ({ row }) => (
          // The row opens the workload; the menu must not, or every action would
          // navigate away from the object it just changed.
          <div className="flex justify-end" onClick={(event) => event.stopPropagation()}>
            <WorkloadActions
              target={{
                clusterId,
                clusterName,
                namespace,
                workload: row.original,
              }}
              canUpdate={canUpdate}
              canDelete={canDelete}
            />
          </div>
        ),
      },
    ],
    [resource, clusterId, clusterName, namespace, canUpdate, canDelete],
  );

  if (detailName) {
    return (
      <WorkloadDetailView
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        resource={resource}
        name={detailName}
        canUpdate={canUpdate}
        canDelete={canDelete}
        onBack={() => setDetailName(null)}
      />
    );
  }

  const nextToken = workloads.data?.continue_token ?? "";

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionTitle
        title={`工作负载 · ${clusterName} / ${namespace}`}
        description="伸缩、滚动重启、CronJob 暂停/恢复和删除都先执行 Kubernetes 服务端 DryRun，再由操作者确认。创建工作负载的表单尚未实现。"
      />
      <Tabs
        value={resource}
        onValueChange={(value) => {
          setResource(value as KubernetesWorkloadResource);
          setDetailName(null);
        }}
        className="flex min-h-0 flex-1 flex-col"
      >
        <TabsList className="w-fit">
          {WORKLOAD_TYPES.map((type) => (
            <TabsTrigger key={type.resource} value={type.resource}>
              {type.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value={resource} className="flex min-h-0 flex-1 flex-col">
          <DataTable
            columns={columns}
            data={workloads.data?.workloads}
            isLoading={workloads.isLoading}
            isFetching={workloads.isFetching}
            error={workloads.error}
            onRetry={() => void workloads.refetch()}
            onRowClick={(workload) => setDetailName(workload.name)}
            rowKey={(workload) => workload.uid || workload.name}
            emptyTitle={`该命名空间没有 ${kindLabel(resource)}`}
            emptyDescription={`${namespace} 中没有可见的 ${kindLabel(resource)}。`}
            toolbar={
              <ContinuePager
                pageIndex={pager.pageIndex}
                nextToken={nextToken}
                onPrevious={pager.goPrevious}
                onNext={pager.goNext}
              />
            }
          />
        </TabsContent>
      </Tabs>
    </div>
  );
}

/**
 * The one column that differs by type. A Deployment's progress is replicas, a
 * Job's is completions and a CronJob's is its schedule — showing all three for
 * every type would leave two of them empty in every row.
 */
function progressColumn(
  resource: KubernetesWorkloadResource,
): ColumnDef<KubernetesWorkloadSummary, unknown> {
  if (resource === "jobs") {
    return {
      header: "完成",
      size: 160,
      cell: ({ row }) => <JobCell job={row.original.job} />,
    };
  }
  if (resource === "cronjobs") {
    return {
      header: "调度",
      size: 190,
      cell: ({ row }) => <CronJobCell cronJob={row.original.cron_job} />,
    };
  }
  return {
    header: "副本",
    size: 150,
    cell: ({ row }) => <ReplicaCell replicas={row.original.replicas} />,
  };
}

function ReplicaCell({ replicas }: { replicas: ReplicaStatus | undefined }) {
  if (!replicas) {
    return <span className="text-subtle-foreground text-xs">—</span>;
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="zke-tnum text-foreground">
        {replicas.ready} / {replicas.desired}
      </span>
      <span className="text-subtle-foreground text-xs">
        已更新 {replicas.updated} · 可用 {replicas.available}
      </span>
    </div>
  );
}

function JobCell({ job }: { job: JobStatus | undefined }) {
  if (!job) {
    return <span className="text-subtle-foreground text-xs">—</span>;
  }
  return (
    <div className="flex flex-col gap-0.5">
      {/* An unset `completions` means the Job finishes as soon as one Pod
          succeeds, so there is no denominator to show rather than a 1 to
          invent. */}
      <span className="zke-tnum text-foreground">
        {job.completions === undefined ? job.succeeded : `${job.succeeded} / ${job.completions}`}
      </span>
      <span className="text-subtle-foreground text-xs">
        活跃 {job.active} · 失败 {job.failed}
      </span>
    </div>
  );
}

function CronJobCell({ cronJob }: { cronJob: CronJobStatus | undefined }) {
  if (!cronJob) {
    return <span className="text-subtle-foreground text-xs">—</span>;
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="zke-mono text-foreground text-xs">{cronJob.schedule}</span>
      <span className="text-subtle-foreground text-xs">
        活跃 {cronJob.active} · 上次调度 {formatAbsolute(cronJob.last_schedule_time)}
      </span>
    </div>
  );
}

/**
 * The images of a workload's Pod template, deduplicated and sorted by the
 * Server. The first one is what identifies the workload at a glance; the rest
 * are counted rather than listed, because a sidecar-heavy Pod would otherwise
 * push every other column off the table.
 */
function ImageCell({ images }: { images: string[] }) {
  if (images.length === 0) {
    return <span className="text-subtle-foreground text-xs">—</span>;
  }
  return (
    // Wrapped rather than truncated: the table lays itself out automatically, so
    // an ellipsis would just widen this column until the image fits anyway.
    <div className="flex items-start gap-1.5">
      <span className="zke-mono text-muted-foreground text-xs break-all" title={images.join("\n")}>
        {images[0]}
      </span>
      {images.length > 1 ? <Badge tone="neutral">+{images.length - 1}</Badge> : null}
    </div>
  );
}

function WorkloadDetailView({
  clusterId,
  clusterName,
  namespace,
  resource,
  name,
  canUpdate,
  canDelete,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesWorkloadResource;
  name: string;
  canUpdate: boolean;
  canDelete: boolean;
  onBack: () => void;
}) {
  const detail = useWorkload(clusterId, namespace, resource, name);

  return (
    <div className="grid gap-3">
      <SectionTitle
        title={name}
        description={`读取自集群 ${clusterName} 的 ${namespace}，仅展示 Kubernetes 返回的当前状态。`}
        actions={
          <div className="flex items-center gap-2">
            {/* The actions need the object they act on, so they appear once the
                detail has actually loaded — a deletion pinned to a UID cannot be
                offered before the UID is known. */}
            {detail.data ? (
              <WorkloadActions
                target={{ clusterId, clusterName, namespace, workload: detail.data }}
                canUpdate={canUpdate}
                canDelete={canDelete}
                variant="buttons"
                onDeleted={onBack}
              />
            ) : null}
            <Button size="sm" variant="secondary" onClick={onBack}>
              <ArrowLeft />
              返回列表
            </Button>
          </div>
        }
      />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !detail.data ? (
        <LoadingState />
      ) : (
        <WorkloadDetailCards workload={detail.data} />
      )}
    </div>
  );
}

function WorkloadDetailCards({ workload }: { workload: KubernetesWorkloadDetail }) {
  const configuration = configurationRows(workload);

  return (
    <div className="grid gap-3 md:grid-cols-2">
      <DetailCard title="概览">
        <DetailRow label="名称" value={workload.name} />
        <DetailRow
          label="类型"
          value={
            <span className="zke-mono text-xs">
              {workload.api_version} · {workload.kind}
            </span>
          }
        />
        <DetailRow label="命名空间" value={workload.namespace} />
        <DetailRow
          label="UID"
          value={<span className="zke-mono text-xs break-all">{workload.uid || "—"}</span>}
        />
        <DetailRow
          label="版本"
          value={<span className="zke-mono text-xs">{workload.resource_version || "—"}</span>}
        />
        <DetailRow label="状态" value={<StatusBadge kind="workload" value={workload.status} />} />
        {/* Job and CronJob v1 Status carry no observedGeneration, so the Server
            returns 0 for them; showing it as "已观察 0" would read as a
            controller that has never looked at the object. */}
        <DetailRow
          label="Generation"
          value={
            workload.api_version === "apps/v1"
              ? `${workload.generation}（已观察 ${workload.observed_generation}）`
              : String(workload.generation)
          }
        />
        <DetailRow label="创建时间" value={formatAbsolute(workload.creation_timestamp)} />
      </DetailCard>

      {workload.replicas ? (
        <DetailCard title="副本">
          <DetailRow label="期望" value={<Count value={workload.replicas.desired} />} />
          <DetailRow label="当前" value={<Count value={workload.replicas.current} />} />
          <DetailRow label="已更新" value={<Count value={workload.replicas.updated} />} />
          <DetailRow label="就绪" value={<Count value={workload.replicas.ready} />} />
          <DetailRow label="可用" value={<Count value={workload.replicas.available} />} />
          <DetailRow label="不可用" value={<Count value={workload.replicas.unavailable} />} />
        </DetailCard>
      ) : null}

      {workload.job ? (
        <DetailCard title="Job 状态">
          <DetailRow
            label="并行度"
            value={
              workload.job.parallelism === undefined ? (
                "—"
              ) : (
                <Count value={workload.job.parallelism} />
              )
            }
          />
          <DetailRow
            label="完成数"
            value={
              workload.job.completions === undefined ? (
                "—"
              ) : (
                <Count value={workload.job.completions} />
              )
            }
          />
          <DetailRow label="活跃" value={<Count value={workload.job.active} />} />
          <DetailRow label="成功" value={<Count value={workload.job.succeeded} />} />
          <DetailRow label="失败" value={<Count value={workload.job.failed} />} />
          <DetailRow label="开始时间" value={formatAbsolute(workload.job.start_time)} />
          <DetailRow label="完成时间" value={formatAbsolute(workload.job.completion_time)} />
        </DetailCard>
      ) : null}

      {workload.cron_job ? (
        <DetailCard title="CronJob 状态">
          <DetailRow
            label="调度"
            value={<span className="zke-mono text-xs">{workload.cron_job.schedule}</span>}
          />
          <DetailRow label="暂停" value={workload.cron_job.suspend ? "是" : "否"} />
          <DetailRow label="活跃 Job" value={<Count value={workload.cron_job.active} />} />
          <DetailRow
            label="上次调度"
            value={formatAbsolute(workload.cron_job.last_schedule_time)}
          />
          <DetailRow
            label="上次成功"
            value={formatAbsolute(workload.cron_job.last_successful_time)}
          />
        </DetailCard>
      ) : null}

      {configuration.length > 0 ? (
        <DetailCard title="配置">
          {configuration.map((row) => (
            <DetailRow key={row.label} label={row.label} value={row.value} />
          ))}
        </DetailCard>
      ) : null}

      <DetailCard title="容器">
        {workload.init_containers.length === 0 && workload.containers.length === 0 ? (
          <DetailRow label="容器" value="—" />
        ) : (
          <>
            {workload.init_containers.map((container) => (
              <DetailRow
                key={`init/${container.name}`}
                label={`Init · ${container.name}`}
                value={
                  <ContainerImage image={container.image} policy={container.image_pull_policy} />
                }
              />
            ))}
            {workload.containers.map((container) => (
              <DetailRow
                key={container.name}
                label={container.name}
                value={
                  <ContainerImage image={container.image} policy={container.image_pull_policy} />
                }
              />
            ))}
          </>
        )}
      </DetailCard>

      <DetailCard title="选择器">
        {!workload.selector ? (
          <DetailRow label="选择器" value="—" />
        ) : (
          <>
            <DetailKeyValues entries={workload.selector.match_labels} />
            {workload.selector.match_expressions.map((expression) => (
              <DetailRow
                key={`${expression.key}/${expression.operator}`}
                label={expression.operator}
                value={
                  <span className="zke-mono text-xs break-all">
                    {expression.key}
                    {expression.values.length > 0 ? ` ∈ ${expression.values.join(", ")}` : ""}
                  </span>
                }
              />
            ))}
          </>
        )}
      </DetailCard>

      <DetailCard title="条件">
        {workload.conditions.length === 0 ? (
          <DetailRow label="条件" value="—" />
        ) : (
          workload.conditions.map((condition) => (
            <DetailRow
              key={condition.type}
              label={condition.type}
              value={
                <div className="grid gap-0.5">
                  <span>{condition.status}</span>
                  {condition.reason || condition.message ? (
                    <span className="text-muted-foreground text-xs break-words">
                      {[condition.reason, condition.message].filter(Boolean).join(" · ")}
                    </span>
                  ) : null}
                  <span className="text-subtle-foreground text-xs">
                    {formatAbsolute(condition.last_transition_time)}
                  </span>
                </div>
              }
            />
          ))
        )}
      </DetailCard>

      <DetailCard title="标签">
        <DetailKeyValues entries={workload.labels} />
      </DetailCard>

      <DetailCard title="注解">
        <DetailKeyValues entries={workload.annotations} />
      </DetailCard>
    </div>
  );
}

/**
 * The spec fields that only some workload types carry. Collected rather than
 * spelled out per type so the card holds exactly what the object has, and is
 * dropped entirely when the object has none of them.
 */
function configurationRows(workload: KubernetesWorkloadDetail): { label: string; value: string }[] {
  const rows: { label: string; value: string }[] = [];
  if (workload.strategy) {
    rows.push({ label: "更新策略", value: workload.strategy });
  }
  if (workload.replicas) {
    rows.push({ label: "最小就绪", value: `${workload.min_ready_seconds} 秒` });
  }
  if (workload.revision_history_limit !== undefined) {
    rows.push({ label: "历史版本", value: String(workload.revision_history_limit) });
  }
  if (workload.service_name) {
    rows.push({ label: "Headless Service", value: workload.service_name });
  }
  if (workload.completion_mode) {
    rows.push({ label: "完成模式", value: workload.completion_mode });
  }
  if (workload.concurrency_policy) {
    rows.push({ label: "并发策略", value: workload.concurrency_policy });
  }
  return rows;
}

function Count({ value }: { value: number }) {
  return <span className="zke-tnum">{value}</span>;
}

function ContainerImage({ image, policy }: { image: string; policy: string }) {
  return (
    <div className="grid gap-0.5">
      <span className="zke-mono text-xs break-all">{image || "—"}</span>
      {policy ? <span className="text-subtle-foreground text-xs">{policy}</span> : null}
    </div>
  );
}
