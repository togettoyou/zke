import { useCallback, useEffect, useRef, useState } from "react";
import { Play, Square, SquareTerminal } from "lucide-react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

import { errorMessage } from "@/api/errors";
import { useClusters } from "@/api/queries/clusters";
import { useCreateClusterTerminalSession } from "@/api/queries/cluster-terminal";
import {
  decodeTerminalOutput,
  encodeTerminalInput,
  POD_EXEC_SUBPROTOCOL,
  terminalSocketUrl,
  type PodExecClientMessage,
  type PodExecServerMessage,
} from "@/api/queries/pod-exec";
import { AppShell, ScopeRequired, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { Alert } from "@/components/ui/misc";
import { Button } from "@/components/ui/button";
import { Badge, StatusDot } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSubmissionKey } from "@/lib/use-submission-key";
import { useScopeStore } from "@/scope/scope-store";
import { useWindowStore } from "@/desktop/window-store";

const NAV: AppNavItem[] = [{ id: "terminal", label: "终端", icon: SquareTerminal }];
type ConnectionState = "idle" | "connecting" | "open" | "closed";

export function TerminalApp({ windowId }: Pick<AppComponentProps, "windowId">) {
  const scope = useScopeStore((state) => state.scope);
  const active = useWindowStore(
    (state) => state.focusedId === windowId && state.windows[windowId]?.mode !== "minimized",
  );
  const clusters = useClusters(scope.projectId, { limit: 100, offset: 0, status: "active" });
  const online = (clusters.data?.clusters ?? []).filter(
    (item) => item.connection.status === "online",
  );
  const [selectedCluster, setSelectedCluster] = useState("");
  const clusterId = online.some((item) => item.id === selectedCluster)
    ? selectedCluster
    : (online[0]?.id ?? "");
  const clusterName = online.find((item) => item.id === clusterId)?.name ?? clusterId;
  if (!scope.projectId) return <ScopeRequired />;

  return (
    <AppShell
      nav={NAV}
      activeId="terminal"
      onNavigate={() => undefined}
      scope={clusterId ? clusterName : undefined}
      toolbar={
        <>
          <span className="text-muted-foreground text-xs">目标集群</span>
          <Select
            value={clusterId}
            onValueChange={setSelectedCluster}
            disabled={clusters.isLoading}
          >
            <SelectTrigger className="w-64" aria-label="目标集群">
              <SelectValue placeholder="选择集群" />
            </SelectTrigger>
            <SelectContent>
              {online.map((item) => (
                <SelectItem key={item.id} value={item.id}>
                  {item.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </>
      }
    >
      {clusterId ? (
        <ClusterTerminalPanel
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          active={active}
        />
      ) : (
        <div className="text-muted-foreground p-6 text-sm">请选择一个在线集群。</div>
      )}
    </AppShell>
  );
}

function ClusterTerminalPanel({
  clusterId,
  clusterName,
  active,
}: {
  clusterId: string;
  clusterName: string;
  active: boolean;
}) {
  const [state, setState] = useState<ConnectionState>("idle");
  const [confirming, setConfirming] = useState(false);
  const create = useCreateClusterTerminalSession();
  const key = useSubmissionKey(confirming);
  const surfaceRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const socketRef = useRef<WebSocket | null>(null);
  const createAbortRef = useRef<AbortController | null>(null);
  const refitRef = useRef<(() => void) | null>(null);
  const attemptRef = useRef(0);

  const send = useCallback((message: PodExecClientMessage) => {
    if (socketRef.current?.readyState === WebSocket.OPEN)
      socketRef.current.send(JSON.stringify(message));
  }, []);

  useEffect(() => {
    const surface = surfaceRef.current;
    if (!surface) return;
    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 12,
      scrollback: 5_000,
      fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
      theme: { background: "#0b0d12", foreground: "#e6e8ee" },
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(surface);
    terminalRef.current = terminal;
    fitRef.current = fit;

    let fitFrame = 0;
    let refreshFrame = 0;
    const fitVisibleTerminal = () => {
      const bounds = surface.getBoundingClientRect();
      if (bounds.width <= 0 || bounds.height <= 0 || !surface.isConnected) return;
      try {
        fit.fit();
        cancelAnimationFrame(refreshFrame);
        refreshFrame = requestAnimationFrame(() => {
          if (surface.getClientRects().length > 0 && terminal.rows > 0) {
            terminal.refresh(0, terminal.rows - 1);
          }
        });
      } catch {
        /* view is closing */
      }
    };
    const scheduleFit = () => {
      cancelAnimationFrame(fitFrame);
      fitFrame = requestAnimationFrame(fitVisibleTerminal);
    };
    const resizeObserver = new ResizeObserver(([entry]) => {
      if (entry && entry.contentRect.width > 0 && entry.contentRect.height > 0) scheduleFit();
    });
    resizeObserver.observe(surface);
    const visibilityObserver = new IntersectionObserver(([entry]) => {
      if (entry?.isIntersecting) scheduleFit();
    });
    visibilityObserver.observe(surface);
    const handleVisibilityChange = () => {
      if (!document.hidden) scheduleFit();
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);
    refitRef.current = scheduleFit;
    scheduleFit();
    const input = terminal.onData((data) =>
      encodeTerminalInput(data).forEach((chunk) => send({ type: "stdin", data: chunk })),
    );
    const resize = terminal.onResize(({ cols, rows }) =>
      send({ type: "resize", columns: cols, rows }),
    );
    return () => {
      cancelAnimationFrame(fitFrame);
      cancelAnimationFrame(refreshFrame);
      resizeObserver.disconnect();
      visibilityObserver.disconnect();
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      refitRef.current = null;
      input.dispose();
      resize.dispose();
      terminal.dispose();
      terminalRef.current = null;
    };
  }, [send]);

  useEffect(() => {
    if (active) refitRef.current?.();
  }, [active]);

  useEffect(
    () => () => {
      attemptRef.current += 1;
      createAbortRef.current?.abort("terminal window closed");
      createAbortRef.current = null;
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
    const terminal = terminalRef.current;
    if (!terminal) return;
    fitRef.current?.fit();
    setState("connecting");
    setConfirming(false);
    const attempt = ++attemptRef.current;
    createAbortRef.current?.abort("superseded terminal connection");
    const controller = new AbortController();
    createAbortRef.current = controller;
    void create
      .mutateAsync({
        clusterId,
        columns: terminal.cols,
        rows: terminal.rows,
        idempotencyKey: key,
        signal: controller.signal,
      })
      .then((ticket) => {
        if (attemptRef.current !== attempt) return;
        const socket = new WebSocket(terminalSocketUrl(ticket.websocket_path), [
          ticket.subprotocol || POD_EXEC_SUBPROTOCOL,
        ]);
        socketRef.current = socket;
        socket.onopen = () => {
          if (attemptRef.current !== attempt) return socket.close();
          setState("open");
          terminal.focus();
          send({ type: "resize", columns: terminal.cols, rows: terminal.rows });
        };
        socket.onmessage = (event) => {
          if (attemptRef.current !== attempt) return;
          let message: PodExecServerMessage;
          try {
            message = JSON.parse(event.data as string) as PodExecServerMessage;
          } catch {
            return;
          }
          if ((message.type === "stdout" || message.type === "stderr") && message.data) {
            terminal.write(decodeTerminalOutput(message.data));
          }
          if (message.type === "exit")
            terminal.writeln(
              `\r\n\u001b[90m[ZKE] ${message.message || message.reason || "会话已结束"}\u001b[0m`,
            );
        };
        socket.onclose = () => {
          if (attemptRef.current === attempt) {
            socketRef.current = null;
            setState("closed");
          }
        };
        socket.onerror = () => {
          if (attemptRef.current === attempt) setState("closed");
        };
      })
      .catch(() => {
        if (attemptRef.current === attempt) setState("closed");
      })
      .finally(() => {
        if (createAbortRef.current === controller) createAbortRef.current = null;
      });
  };

  return (
    <div className="flex h-full min-h-0 flex-col p-3">
      <div className="mb-3 flex items-center gap-2">
        <ConnectionBadge state={state} />
        <span className="text-muted-foreground text-xs">
          权限来自当前角色，会话结束后自动删除临时 Pod 与授权。
        </span>
        <div className="ml-auto">
          {state === "open" ? (
            <Button size="sm" variant="secondary" className="text-danger" onClick={disconnect}>
              <Square />
              断开
            </Button>
          ) : (
            <Button
              size="sm"
              variant="primary"
              disabled={state === "connecting"}
              onClick={() => setConfirming(true)}
            >
              <Play />
              {state === "connecting" ? "正在创建…" : state === "closed" ? "重新连接" : "连接"}
            </Button>
          )}
        </div>
      </div>
      {create.error ? (
        <Alert tone="danger" className="mb-3">
          {errorMessage(create.error)}
        </Alert>
      ) : null}
      {state === "connecting" ? (
        <Alert tone="info" className="mb-3">
          {
            "正在创建终端 Pod，首次拉取镜像可能需要一些时间。可以切换或最小化窗口；关闭终端窗口将取消创建并清理临时资源。"
          }
        </Alert>
      ) : null}
      <div className="border-border rounded-panel min-h-0 flex-1 overflow-hidden border bg-[#0b0d12] p-2">
        <div ref={surfaceRef} className="h-full w-full" />
      </div>
      <SensitiveActionDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="打开集群终端"
        description="ZKE 将在 zke-system 创建短生命周期终端 Pod，并按当前角色向业务命名空间投射 Kubernetes RBAC。"
        scopeLines={[{ label: "集群", name: clusterName, id: clusterId }]}
        impacts={[
          "kubectl 可访问现有业务命名空间，但只能执行当前角色对应的操作；未持有 Secret 权限时无法读取 Secret。",
          "zke-system 不授予业务资源访问权；会话不使用 Agent ServiceAccount，结束或超时后清理临时资源。",
          "终端命令可能直接修改集群资源，请确认目标集群。",
        ]}
        confirmLabel="打开终端"
        destructive
        pending={create.isPending}
        error={create.error}
        onConfirm={connect}
      />
    </div>
  );
}

function ConnectionBadge({ state }: { state: ConnectionState }) {
  const open = state === "open";
  return (
    <Badge tone={open ? "success" : state === "connecting" ? "info" : "neutral"}>
      <StatusDot tone={open ? "success" : state === "connecting" ? "info" : "neutral"} />
      {open
        ? "已连接"
        : state === "connecting"
          ? "连接中"
          : state === "closed"
            ? "已断开"
            : "未连接"}
    </Badge>
  );
}
