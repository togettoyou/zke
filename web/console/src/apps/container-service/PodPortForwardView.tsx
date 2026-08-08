import { useEffect, useRef, useState } from "react";
import { Cable, Square } from "lucide-react";

import { errorMessage } from "@/api/errors";
import {
  POD_PORT_FORWARD_SUBPROTOCOL,
  podPortForwardSocketUrl,
  useCreatePodPortForwardSession,
  type PodPortForwardStatus,
} from "@/api/queries/pod-port-forward";
import { usePod } from "@/api/queries/pods";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import { useSubmissionKey } from "@/lib/use-submission-key";

const MAX_PREVIEW_CHARACTERS = 2 * 1024 * 1024;

function isSafeHttpPath(value: string): boolean {
  return (
    value.startsWith("/") &&
    value.length <= 2_048 &&
    !Array.from(value).some((character) => {
      const code = character.codePointAt(0) ?? 0;
      return code <= 32 || code === 127;
    })
  );
}

export function PodPortForwardView({
  clusterId,
  clusterName,
  namespace,
  podName,
  podUid,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  podName: string;
  podUid: string;
  onBack: () => void;
}) {
  const detail = usePod(clusterId, namespace, podName);
  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader title={`${podName} · 端口转发`} onBack={onBack} />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !detail.data ? (
        <LoadingState />
      ) : detail.data.uid !== podUid ? (
        <Alert tone="danger">
          该 Pod 已被同名重新创建，本次入口绑定的 UID 已失效。请返回列表重新打开。
        </Alert>
      ) : (
        <PortPreview
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          podName={podName}
          podUid={podUid}
        />
      )}
    </div>
  );
}

function PortPreview({
  clusterId,
  clusterName,
  namespace,
  podName,
  podUid,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  podName: string;
  podUid: string;
}) {
  const [port, setPort] = useState("8080");
  const [path, setPath] = useState("/");
  const [confirming, setConfirming] = useState(false);
  const [state, setState] = useState<"idle" | "connecting" | "open" | "closed">("idle");
  const [response, setResponse] = useState("");
  const [status, setStatus] = useState<PodPortForwardStatus | null>(null);
  const socketRef = useRef<WebSocket | null>(null);
  const attemptRef = useRef(0);
  const decoderRef = useRef(new TextDecoder());
  const createSession = useCreatePodPortForwardSession();
  const ticketKey = useSubmissionKey(confirming);
  const portNumber = Number(port);
  const validPort = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65_535;
  const validPath = isSafeHttpPath(path);

  useEffect(
    () => () => {
      attemptRef.current += 1;
      socketRef.current?.close(1000, "view closed");
    },
    [],
  );

  const disconnect = () => {
    attemptRef.current += 1;
    socketRef.current?.close(1000, "closed by operator");
    socketRef.current = null;
    setState("closed");
  };

  const connect = () => {
    if (!validPort || !validPath) return;
    setState("connecting");
    setResponse("");
    setStatus(null);
    decoderRef.current = new TextDecoder();
    const attempt = ++attemptRef.current;
    void createSession
      .mutateAsync({
        clusterId,
        namespace,
        podName,
        uid: podUid,
        port: portNumber,
        idempotencyKey: ticketKey,
      })
      .then((session) => {
        if (attempt !== attemptRef.current) return;
        const socket = new WebSocket(podPortForwardSocketUrl(session.websocket_path), [
          POD_PORT_FORWARD_SUBPROTOCOL,
        ]);
        socket.binaryType = "arraybuffer";
        socketRef.current = socket;
        socket.onopen = () => {
          if (attempt !== attemptRef.current || socket.protocol !== POD_PORT_FORWARD_SUBPROTOCOL) {
            socket.close(1002, "subprotocol mismatch");
            return;
          }
          setState("open");
          socket.send(
            new TextEncoder().encode(
              `GET ${path} HTTP/1.1\r\nHost: ${podName}:${portNumber}\r\nConnection: close\r\nAccept: */*\r\n\r\n`,
            ),
          );
        };
        socket.onmessage = (event) => {
          if (attempt !== attemptRef.current) return;
          if (event.data instanceof ArrayBuffer) {
            const text = decoderRef.current.decode(new Uint8Array(event.data), { stream: true });
            setResponse((current) => {
              const next = current + text;
              if (next.length <= MAX_PREVIEW_CHARACTERS) return next;
              socket.close(1009, "preview limit reached");
              return `${next.slice(0, MAX_PREVIEW_CHARACTERS)}\n\n[Console 预览已达到 2 MiB，连接已关闭]`;
            });
            return;
          }
          try {
            setStatus(JSON.parse(String(event.data)) as PodPortForwardStatus);
          } catch {
            setStatus({ type: "exit", result: "internal", message: "服务端返回了无效的状态消息" });
          }
        };
        socket.onclose = () => {
          if (attempt === attemptRef.current) {
            socketRef.current = null;
            setState("closed");
          }
        };
        socket.onerror = () => {
          if (attempt === attemptRef.current) setState("closed");
        };
      })
      .catch(() => setState("closed"));
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col px-5 pb-5">
      <Alert tone="info" className="mb-4">
        Console 通过受限二进制隧道发送一次 HTTP GET 并显示原始响应；不会在 Agent 上暴露监听端口。 非
        HTTP 协议可直接使用同一 WebSocket API 传输原始 TCP 字节。
      </Alert>
      <div className="border-border rounded-panel mb-4 flex flex-wrap items-end gap-3 border p-4">
        <div className="grid gap-1.5">
          <Label htmlFor="port-forward-port">Pod 端口</Label>
          <Input
            id="port-forward-port"
            className="w-32"
            inputMode="numeric"
            value={port}
            disabled={state === "open" || state === "connecting"}
            onChange={(event) => setPort(event.target.value)}
          />
        </div>
        <div className="grid min-w-56 flex-1 gap-1.5">
          <Label htmlFor="port-forward-path">HTTP 路径</Label>
          <Input
            id="port-forward-path"
            value={path}
            disabled={state === "open" || state === "connecting"}
            onChange={(event) => setPath(event.target.value)}
          />
        </div>
        {state === "open" ? (
          <Button variant="secondary" className="text-danger" onClick={disconnect}>
            <Square />
            断开
          </Button>
        ) : (
          <Button
            variant="primary"
            disabled={!validPort || !validPath || state === "connecting"}
            onClick={() => setConfirming(true)}
          >
            <Cable />
            {state === "connecting" ? "连接中…" : "连接并请求"}
          </Button>
        )}
      </div>
      {createSession.error ? (
        <Alert tone="danger" className="mb-3">
          {errorMessage(createSession.error)}
        </Alert>
      ) : null}
      <pre className="border-border bg-surface-muted rounded-panel min-h-0 flex-1 overflow-auto border p-4 text-xs break-all whitespace-pre-wrap">
        {response || "响应将显示在这里。"}
      </pre>
      <div className="text-subtle-foreground mt-3 flex flex-wrap items-center gap-3 text-xs">
        <Badge tone={state === "open" ? "success" : state === "connecting" ? "warning" : "neutral"}>
          {state === "open"
            ? "已连接"
            : state === "connecting"
              ? "连接中"
              : state === "closed"
                ? "已结束"
                : "未连接"}
        </Badge>
        {status ? (
          <span>
            {status.result === "ok"
              ? "转发已结束"
              : status.message || status.reason || status.result}{" "}
            · 上行 {status.client_bytes ?? 0} B / 下行 {status.pod_bytes ?? 0} B
          </span>
        ) : null}
      </div>
      <SensitiveActionDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="确认打开 Pod 端口转发"
        description="该操作会建立到 Pod 内指定 TCP 端口的临时数据通道。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "Pod", name: podName, id: podUid },
          { label: "远端端口", name: String(portNumber) },
        ]}
        impacts={[
          "通道绑定当前登录 Session、Pod UID 和单个端口，只能使用一次。",
          "访问结果取决于 Pod 内进程；传输正文不会写入日志或审计。",
        ]}
        confirmLabel="连接并请求"
        pending={createSession.isPending}
        error={createSession.error}
        onConfirm={() => {
          setConfirming(false);
          connect();
        }}
      />
    </div>
  );
}
