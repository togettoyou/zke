import { useLayoutEffect, useRef, useState } from "react";
import { Download, Pause, Play, RotateCw } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { usePodLogStream } from "@/api/queries/pod-logs";
import { usePod } from "@/api/queries/pods";
import type { KubernetesPodDetail } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { CopyButton } from "@/components/common/status";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Alert, Checkbox } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const TAIL_OPTIONS = [100, 200, 500, 1000, 5000];

/** Kubernetes accepts up to seven days; these are the windows worth one click. */
const SINCE_OPTIONS: { value: string; label: string; seconds?: number }[] = [
  { value: "all", label: "不限" },
  { value: "300", label: "最近 5 分钟", seconds: 300 },
  { value: "3600", label: "最近 1 小时", seconds: 3_600 },
  { value: "86400", label: "最近 1 天", seconds: 86_400 },
];

/**
 * One container's logs, as a snapshot or a live stream.
 *
 * It is a view rather than a dialog because a log is read, not filled in: it
 * wants the whole window, and it has to stay open while the operator scrolls
 * back through it.
 */
export function PodLogsView({
  clusterId,
  namespace,
  podName,
  podUid,
  initialContainer,
  initialPrevious = false,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  podName: string;
  podUid: string;
  initialContainer?: string;
  initialPrevious?: boolean;
  onBack: () => void;
}) {
  const detail = usePod(clusterId, namespace, podName);
  const pod = detail.data;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader title={`${podName} · 日志`} onBack={onBack} />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !pod ? (
        <LoadingState />
      ) : pod.uid !== podUid ? (
        <div className="p-3">
          <Alert tone="warning">
            该名称对应的 Pod 已被重新创建。请返回列表并重新打开日志，避免在同一视图中切换到新的 Pod
            实例。
          </Alert>
        </div>
      ) : (
        <PodLogReader
          clusterId={clusterId}
          namespace={namespace}
          pod={pod}
          podUid={podUid}
          initialContainer={initialContainer}
          initialPrevious={initialPrevious}
        />
      )}
    </div>
  );
}

type ContainerChoice = { name: string; group: string };

function containerChoices(pod: KubernetesPodDetail): ContainerChoice[] {
  return [
    ...pod.containers.map((container) => ({ name: container.name, group: "容器" })),
    ...pod.init_containers.map((container) => ({ name: container.name, group: "初始化容器" })),
    ...pod.ephemeral_containers.map((container) => ({ name: container.name, group: "临时容器" })),
  ];
}

function PodLogReader({
  clusterId,
  namespace,
  pod,
  podUid,
  initialContainer,
  initialPrevious,
}: {
  clusterId: string;
  namespace: string;
  pod: KubernetesPodDetail;
  podUid: string;
  initialContainer?: string;
  initialPrevious: boolean;
}) {
  const choices = containerChoices(pod);
  const [container, setContainer] = useState(
    choices.some((choice) => choice.name === initialContainer)
      ? (initialContainer as string)
      : (choices[0]?.name ?? ""),
  );
  const [follow, setFollow] = useState(false);
  const [previous, setPrevious] = useState(initialPrevious);
  const [timestamps, setTimestamps] = useState(false);
  const [tailLines, setTailLines] = useState(200);
  const [since, setSince] = useState("all");

  const selectedContainer = choices.some((choice) => choice.name === container)
    ? container
    : (choices[0]?.name ?? "");
  const sinceSeconds = SINCE_OPTIONS.find((option) => option.value === since)?.seconds;
  const stream = usePodLogStream(
    selectedContainer
      ? {
          clusterId,
          namespace,
          podName: pod.name,
          uid: podUid,
          container: selectedContainer,
          // Following a terminated instance is meaningless: it produces nothing
          // more, and Kubernetes rejects the combination.
          follow: follow && !previous,
          previous,
          timestamps,
          tailLines,
          ...(sinceSeconds === undefined ? {} : { sinceSeconds }),
        }
      : null,
  );
  const activelyFollowing =
    follow && !previous && (stream.status === "loading" || stream.status === "streaming");

  const surfaceRef = useRef<HTMLDivElement>(null);
  const [stickToBottom, setStickToBottom] = useState(true);

  // Follow the tail only while the operator is already at it. Yanking the view
  // back down while they are reading earlier lines is the one thing a log
  // viewer must not do.
  useLayoutEffect(() => {
    const surface = surfaceRef.current;
    if (surface && stickToBottom) {
      surface.scrollTop = surface.scrollHeight;
    }
  }, [stream.pages, stickToBottom]);

  const grouped = choices.reduce<Record<string, string[]>>((accumulator, choice) => {
    (accumulator[choice.group] ??= []).push(choice.name);
    return accumulator;
  }, {});

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <div className="grid content-start gap-1.5">
          <Label htmlFor="pod-log-container">容器</Label>
          <Select value={selectedContainer} onValueChange={setContainer}>
            <SelectTrigger id="pod-log-container" className="w-56">
              <SelectValue placeholder="选择容器" />
            </SelectTrigger>
            <SelectContent>
              {Object.entries(grouped).map(([group, names]) => (
                <SelectGroup key={group}>
                  <SelectLabel>{group}</SelectLabel>
                  {names.map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="grid content-start gap-1.5">
          <Label htmlFor="pod-log-tail">尾部行数</Label>
          <Select value={String(tailLines)} onValueChange={(value) => setTailLines(Number(value))}>
            <SelectTrigger id="pod-log-tail" className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {TAIL_OPTIONS.map((option) => (
                <SelectItem key={option} value={String(option)}>
                  {option} 行
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="grid content-start gap-1.5">
          <Label htmlFor="pod-log-since">时间范围</Label>
          <Select value={since} onValueChange={setSince}>
            <SelectTrigger id="pod-log-since" className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SINCE_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <label className="flex h-9 items-center gap-2 text-[13px]">
          <Checkbox
            checked={timestamps}
            onCheckedChange={(checked) => setTimestamps(checked === true)}
          />
          时间戳
        </label>

        <label className="flex h-9 items-center gap-2 text-[13px]">
          <Checkbox
            checked={previous}
            onCheckedChange={(checked) => {
              setPrevious(checked === true);
              if (checked === true) {
                setFollow(false);
              }
            }}
          />
          上一个实例
        </label>

        <div className="ml-auto flex items-center gap-2">
          <Button
            size="sm"
            variant={activelyFollowing ? "primary" : "secondary"}
            disabled={previous}
            onClick={() => {
              if (activelyFollowing) {
                // Stopping keeps what has already arrived on screen; only the
                // request behind it is abandoned. It also turns the mode off, so
                // a later reload, container switch or checkbox reads a snapshot
                // rather than quietly opening another follow stream.
                stream.stop();
                setFollow(false);
              } else {
                // Following is always an explicit new attempt: a stopped stream
                // holds on to its request, and a follow that ended on its own
                // has the same options as the one being asked for.
                setFollow(true);
                stream.reload();
              }
              setStickToBottom(true);
            }}
          >
            {activelyFollowing ? <Pause /> : <Play />}
            {activelyFollowing ? "停止跟随" : "实时跟随"}
          </Button>
          <Button size="sm" variant="secondary" onClick={stream.reload}>
            <RotateCw />
            重新加载
          </Button>
          <CopyButton value={stream.readText} label="复制" />
          <Button
            size="sm"
            variant="secondary"
            disabled={stream.empty}
            onClick={() => downloadLogs(pod.name, selectedContainer, stream.readText())}
          >
            <Download />
            下载
          </Button>
        </div>
      </div>

      {previous ? (
        <Alert tone="info">正在读取该容器上一个已终止实例的日志，实时跟随对它不适用。</Alert>
      ) : null}
      {stream.status === "error" ? <Alert tone="danger">{errorMessage(stream.error)}</Alert> : null}

      <div
        ref={surfaceRef}
        onScroll={(event) => {
          const element = event.currentTarget;
          setStickToBottom(element.scrollHeight - element.scrollTop - element.clientHeight < 24);
        }}
        className="border-border bg-surface rounded-panel min-h-0 flex-1 overflow-auto border p-3"
      >
        {stream.status === "loading" ? (
          <LoadingState />
        ) : stream.empty ? (
          <p className="text-muted-foreground p-4 text-center text-[13px]">
            {stream.status === "error" ? "没有读取到日志。" : "该容器在当前范围内没有日志输出。"}
          </p>
        ) : (
          <pre className="zke-mono text-foreground text-xs leading-relaxed break-words whitespace-pre-wrap">
            {/*
             * One block per page rather than one text node for the whole
             * buffer: a text node's content cannot change without the browser
             * re-laying out everything in it, which at the buffer's ceiling is
             * a fifth of a second per chunk. Separate blocks are laid out
             * independently, so an update touches only the page still growing.
             * `span` with `display: block` because `pre` takes phrasing
             * content; the pages inherit its wrapping and font.
             */}
            {stream.pages.map((page, index) => (
              <span
                key={page.id}
                /*
                 * A line longer than a page is sealed mid-line. Those pages
                 * render inline so the browser lays them out as the one line
                 * they are: a block boundary there would break the line at a
                 * column that is not the margin, and put a newline into any
                 * text selected across it.
                 */
                className={
                  (!page.endsLine && index < stream.pages.length - 1) ||
                  stream.pages[index - 1]?.endsLine === false
                    ? "inline"
                    : "block"
                }
              >
                {page.text}
              </span>
            ))}
          </pre>
        )}
      </div>

      <div className="text-subtle-foreground flex flex-wrap items-center gap-3 text-xs">
        <StreamStatus status={stream.status} following={follow && !previous} />
        <span className="zke-tnum">已接收 {formatBytes(stream.bytes)}</span>
        {stream.truncated ? <span>已丢弃较早的日志以限制浏览器内存占用</span> : null}
        {stickToBottom ? null : <span>已暂停自动滚动——滚动到底部可恢复</span>}
      </div>
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

function downloadLogs(podName: string, container: string, text: string): void {
  try {
    const blob = new Blob([text], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `${podName}-${container}.log`;
    anchor.click();
    URL.revokeObjectURL(url);
  } catch {
    toast.error("下载日志失败");
  }
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KiB`;
  }
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}
