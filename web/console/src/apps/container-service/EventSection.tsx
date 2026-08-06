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
import { useResourceTypes } from "@/api/queries/kubernetes-resources";
import { SectionToolbarActions } from "@/apps/AppShell";
import { DataTable } from "@/components/common/data-table";
import { RelativeTime, StatusBadge } from "@/components/common/status";
import { statusLabel } from "@/components/common/status-labels";
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

/** Radix Select cannot hold an empty value, so "any" needs a name of its own. */
const ANY_TYPE = "__any__";
const ANY_KIND = "__any_kind__";
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
  const [kind, setKind] = useState(ANY_KIND);
  const [name, setName] = useState("");
  const [reason, setReason] = useState("");

  // Type and Kind are whole values chosen from a list, so they are sent to the
  // Server and become Field Selector terms on the Watch itself: the snapshot's
  // limit is then spent on events that already match.
  //
  // Name and reason are not. A Field Selector compares exactly, and an operator
  // typing part of a name means part of a name — `api` should find
  // `api-server-7d9f`, and as a selector term it found nothing at all. They are
  // therefore matched here, as substrings of what the stream has delivered, and
  // the fields say so.
  const stream = useKubernetesEventStream({
    clusterId,
    namespace,
    follow,
    limit,
    filters: {
      ...(type === ANY_TYPE ? {} : { type: type as "Normal" | "Warning" }),
      ...(kind === ANY_KIND ? {} : { resourceKind: kind }),
    },
  });

  // The Kind options are every Kind the target Cluster's API Discovery reports — the
  // same catalog the resource browser's tree is built from, so CRDs an operator
  // installed are in it — plus any Kind the events on screen name, which keeps
  // the picker usable when Discovery is unavailable. Not the events alone: a
  // truncated snapshot is exactly when this filter is wanted, and a list built
  // from what is already visible cannot narrow to what is not.
  //
  // Deduplicated by Kind alone, because that is all the field selector compares:
  // one `HorizontalPodAutoscaler` covers both of its versions.
  const catalog = useResourceTypes(clusterId);
  const kindOptions = useMemo(() => {
    const kinds = new Set<string>();
    for (const resourceType of catalog.data?.resources ?? []) {
      if (resourceType.kind) {
        kinds.add(resourceType.kind);
      }
    }
    for (const event of stream.events) {
      if (event.regarding.kind) {
        kinds.add(event.regarding.kind);
      }
    }
    // A filtered stream returns one Kind, and the selection must survive being
    // applied or the trigger would show a value its own list no longer offers.
    if (kind !== ANY_KIND) {
      kinds.add(kind);
    }
    return [...kinds].sort((left, right) => left.localeCompare(right));
  }, [catalog.data, stream.events, kind]);

  // Matched against what is on the wire — the object's own name and Kubernetes'
  // reason — so the two inputs narrow exactly the rows the table is showing.
  const nameNeedle = name.trim().toLowerCase();
  const reasonNeedle = reason.trim().toLowerCase();
  const rows = useMemo(() => {
    if (nameNeedle === "" && reasonNeedle === "") {
      return stream.events;
    }
    return stream.events.filter((event) => {
      const objectName = (event.regarding.name ?? "").toLowerCase();
      return (
        (nameNeedle === "" || objectName.includes(nameNeedle)) &&
        (reasonNeedle === "" || event.reason.toLowerCase().includes(reasonNeedle))
      );
    });
  }, [stream.events, nameNeedle, reasonNeedle]);
  const narrowed = rows.length !== stream.events.length;

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
              {/* Named exactly as the 类型 column names them: the filter and the
                  rows it produces have to be the same word. */}
              <SelectItem value="Warning">{statusLabel("eventType", "Warning")}</SelectItem>
              <SelectItem value="Normal">{statusLabel("eventType", "Normal")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="event-kind">关联资源类型</Label>
          <Select value={kind} onValueChange={setKind}>
            <SelectTrigger id="event-kind" className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ANY_KIND}>全部</SelectItem>
              {kindOptions.map((option) => (
                <SelectItem key={option} value={option}>
                  {option}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="event-name">关联资源名称</Label>
          <Input
            id="event-name"
            value={name}
            className="w-48"
            autoComplete="off"
            spellCheck={false}
            placeholder="模糊匹配"
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
            placeholder="模糊匹配，例如 Failed"
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
        data={rows}
        isLoading={stream.status === "loading"}
        error={stream.status === "error" ? stream.error : undefined}
        onRetry={stream.reload}
        rowKey={(event) => event.uid}
        emptyTitle="没有匹配的事件"
        emptyDescription={
          nameNeedle || reasonNeedle
            ? "已读取的事件中没有匹配名称或原因的；这两项只在已读取的事件中模糊匹配，更早的事件需要放宽类型筛选或提高快照上限后重新加载。"
            : "当前筛选条件下，该命名空间没有 Kubernetes Event。集群会按保留期回收事件。"
        }
      />

      <div className="text-subtle-foreground mt-3 flex flex-wrap items-center gap-3 text-xs">
        <StreamStatus status={stream.status} following={follow} />
        <span className="zke-tnum">
          共 {rows.length} 条{narrowed ? `（已读取 ${stream.events.length} 条）` : ""}
        </span>
        {nameNeedle || reasonNeedle ? (
          // Said where the count is, because the count is what the filter
          // changed: Kubernetes matches these two fields exactly, so the
          // substring form only reaches what the stream already delivered.
          <span>名称与原因在已读取的事件中模糊匹配</span>
        ) : null}
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
