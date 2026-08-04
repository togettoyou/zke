import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Pause, Play, RotateCw } from "lucide-react";

import { errorMessage } from "@/api/errors";
import {
  eventTimestamp,
  useKubernetesEventStream,
  type KubernetesEventRecord,
  type KubernetesEventReference,
} from "@/api/queries/kubernetes-events";
import { SectionToolbarActions } from "@/apps/AppShell";
import { DataTable } from "@/components/common/data-table";
import { RelativeTime, StatusBadge } from "@/components/common/status";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useDebouncedValue } from "@/lib/use-debounced-value";

/** Radix Select cannot hold an empty value, so "any" needs a name of its own. */
const ANY_TYPE = "__any__";
const LIMIT_OPTIONS = [100, 200, 500];

/**
 * Why the Server ended a stream, in the operator's terms.
 *
 * `watch_closed` and `resource_version_expired` are absent on purpose: the
 * stream recovers from both on its own, so they never reach a resting state.
 */
const CLOSE_REASONS: Record<string, string> = {
  completed: "快照已读取完毕",
  limit_reached: "已达到服务端返回上限",
  maximum_duration: "已达到服务端最长持续时间",
  timeout: "上游读取超时",
  canceled: "已取消",
  access_revoked: "权限已被撤销",
  capacity_exhausted: "服务端流容量已耗尽",
  failed: "服务端读取失败",
};

type EventSectionProps = {
  clusterId: string;
  namespace: string;
};

/**
 * Kubernetes Events of one Namespace of one Cluster.
 *
 * Events are read through their own permission and their own protocol rather
 * than the generic Resource stream, which rejects them outright — reading them
 * is not implied by reading the Cluster.
 */
export function EventSection({ clusterId, namespace }: EventSectionProps) {
  const [follow, setFollow] = useState(true);
  const [limit, setLimit] = useState(200);
  const [type, setType] = useState(ANY_TYPE);
  const [kind, setKind] = useState("");
  const [name, setName] = useState("");
  const [reason, setReason] = useState("");

  // Filters are applied by the Server, so every keystroke would otherwise open a
  // new Watch.
  const debouncedKind = useDebouncedValue(kind.trim());
  const debouncedName = useDebouncedValue(name.trim());
  const debouncedReason = useDebouncedValue(reason.trim());

  const stream = useKubernetesEventStream({
    clusterId,
    namespace,
    follow,
    limit,
    filters: {
      ...(type === ANY_TYPE ? {} : { type: type as "Normal" | "Warning" }),
      ...(debouncedKind ? { resourceKind: debouncedKind } : {}),
      ...(debouncedName ? { resourceName: debouncedName } : {}),
      ...(debouncedReason ? { reason: debouncedReason } : {}),
    },
  });

  const columns = useMemo<ColumnDef<KubernetesEventRecord, unknown>[]>(
    () => [
      {
        header: "类型",
        size: 90,
        cell: ({ row }) => <StatusBadge kind="eventType" value={row.original.type} />,
      },
      {
        header: "原因",
        size: 160,
        cell: ({ row }) => (
          <span className="text-foreground font-medium break-words">{row.original.reason}</span>
        ),
      },
      {
        header: "关联对象",
        size: 200,
        cell: ({ row }) => <ObjectCell reference={row.original.regarding} />,
      },
      {
        header: "消息",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-words">{row.original.message}</span>
        ),
      },
      {
        header: "次数",
        size: 70,
        cell: ({ row }) => <span className="zke-tnum">{row.original.count || 1}</span>,
      },
      {
        header: "最近发生",
        size: 130,
        cell: ({ row }) => (
          <RelativeTime value={eventTimestamp(row.original)} className="text-muted-foreground" />
        ),
      },
    ],
    [],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* The stream controls go to the toolbar; the filters stay here, because
          they are fields with labels rather than actions. */}
      <SectionToolbarActions>
        <Button
          size="sm"
          variant={follow ? "primary" : "secondary"}
          onClick={() => {
            if (follow) {
              stream.stop();
            }
            setFollow(!follow);
          }}
        >
          {follow ? <Pause /> : <Play />}
          {follow ? "停止跟随" : "实时跟随"}
        </Button>
        <Button size="sm" variant="secondary" onClick={stream.reload}>
          <RotateCw />
          重新加载
        </Button>
      </SectionToolbarActions>

      <div className="mb-3 flex flex-wrap items-end gap-3">
        <div className="grid content-start gap-1.5">
          <Label htmlFor="event-type">类型</Label>
          <Select value={type} onValueChange={setType}>
            <SelectTrigger id="event-type" className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ANY_TYPE}>全部</SelectItem>
              <SelectItem value="Warning">Warning</SelectItem>
              <SelectItem value="Normal">Normal</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="event-kind">关联资源类型</Label>
          <Input
            id="event-kind"
            value={kind}
            className="w-40"
            autoComplete="off"
            spellCheck={false}
            placeholder="例如 Pod"
            onChange={(event) => setKind(event.target.value)}
          />
        </div>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="event-name">关联资源名称</Label>
          <Input
            id="event-name"
            value={name}
            className="w-48"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setName(event.target.value)}
          />
        </div>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="event-reason">原因</Label>
          <Input
            id="event-reason"
            value={reason}
            className="w-40"
            autoComplete="off"
            spellCheck={false}
            placeholder="例如 Failed"
            onChange={(event) => setReason(event.target.value)}
          />
        </div>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="event-limit">快照上限</Label>
          <Select value={String(limit)} onValueChange={(value) => setLimit(Number(value))}>
            <SelectTrigger id="event-limit" className="w-28">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LIMIT_OPTIONS.map((option) => (
                <SelectItem key={option} value={String(option)}>
                  {option} 条
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      {stream.truncated ? (
        <Alert tone="info" className="mb-3">
          初始快照已被服务端按上限截断，只展示本次返回的 {limit} 条；其他事件需要收窄筛选条件。
        </Alert>
      ) : null}

      <DataTable
        columns={columns}
        data={stream.events}
        isLoading={stream.status === "loading"}
        error={stream.status === "error" ? stream.error : undefined}
        onRetry={stream.reload}
        rowKey={(event) => event.uid}
        emptyTitle="没有匹配的事件"
        emptyDescription="当前筛选条件下，该命名空间没有 Kubernetes Event。集群会按保留期回收事件。"
      />

      <div className="text-subtle-foreground mt-3 flex flex-wrap items-center gap-3 text-xs">
        <StreamStatus status={stream.status} following={follow} />
        <span className="zke-tnum">共 {stream.events.length} 条</span>
        {stream.closeReason ? <span>{closeReasonLabel(stream.closeReason)}</span> : null}
        {stream.status === "error" ? <span>{errorMessage(stream.error)}</span> : null}
      </div>
    </div>
  );
}

function closeReasonLabel(reason: string): string {
  return CLOSE_REASONS[reason] ?? `结束原因：${reason}`;
}

function ObjectCell({ reference }: { reference: KubernetesEventReference }) {
  if (!reference.kind && !reference.name) {
    return <span className="text-subtle-foreground text-xs">—</span>;
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-foreground break-all">{reference.name || "—"}</span>
      <span className="text-subtle-foreground text-xs">
        {reference.kind || "未知类型"}
        {reference.fieldPath ? ` · ${reference.fieldPath}` : ""}
      </span>
    </div>
  );
}

function StreamStatus({ status, following }: { status: string; following: boolean }) {
  if (status === "streaming" && following) {
    return (
      <Badge tone="success">
        <StatusDot tone="success" />
        实时跟随中
      </Badge>
    );
  }
  if (status === "streaming" || status === "loading") {
    return (
      <Badge tone="info">
        <StatusDot tone="info" />
        读取中
      </Badge>
    );
  }
  if (status === "error") {
    return (
      <Badge tone="danger">
        <StatusDot tone="danger" />
        读取失败
      </Badge>
    );
  }
  return (
    <Badge tone="neutral">
      <StatusDot tone="neutral" />
      已结束
    </Badge>
  );
}
