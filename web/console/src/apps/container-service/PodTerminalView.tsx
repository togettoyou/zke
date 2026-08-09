import { useCallback, useEffect, useRef, useState } from "react";
import { History, Play, Square } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

import { errorMessage } from "@/api/errors";
import {
  decodeTerminalOutput,
  encodeTerminalInput,
  POD_EXEC_SUBPROTOCOL,
  terminalSocketUrl,
  useCreateTerminalSession,
  useTerminalRecording,
  useTerminalRecordings,
  type PodExecClientMessage,
  type PodExecServerMessage,
} from "@/api/queries/pod-exec";
import { usePod } from "@/api/queries/pods";
import type { KubernetesPodDetail } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
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
import { useSubmissionKey } from "@/lib/use-submission-key";
import { formatAbsolute } from "@/lib/time";
import { queryKeys } from "@/api/query-keys";

type ConnectionState = "idle" | "connecting" | "open" | "replaying" | "closed";

/** How a session ended, for the line written into the terminal when it does. */
type ExitNotice = { text: string; failed: boolean };

/**
 * An interactive shell in one container of one Pod.
 *
 * Two steps, because a shell is the least reversible thing this Console offers:
 * a one-shot ticket is minted for exactly this Pod UID and container, and the
 * WebSocket presents it. The ticket expires in seconds and cannot be reused, so
 * reconnecting means asking again — there is no long-lived credential here.
 */
export function PodTerminalView({
  clusterId,
  clusterName,
  namespace,
  podName,
  podUid,
  canRecord,
  canReadRecordings,
  active,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  podName: string;
  /** Pinned when the entry was opened, so a rebuilt Pod cannot be attached to. */
  podUid: string;
  canRecord: boolean;
  canReadRecordings: boolean;
  /** Refits and repaints xterm when this application's window returns to front. */
  active: boolean;
  onBack: () => void;
}) {
  const detail = usePod(clusterId, namespace, podName);
  const pod = detail.data;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader title={`${podName} · 终端`} onBack={onBack} />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !pod ? (
        <LoadingState />
      ) : pod.uid !== podUid ? (
        <Alert tone="danger">
          该 Pod 已被删除并以同名重新创建，本次终端入口绑定的 UID 已不存在。请返回列表重新打开。
        </Alert>
      ) : (
        <TerminalSession
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          pod={pod}
          canRecord={canRecord}
          canReadRecordings={canReadRecordings}
          active={active}
        />
      )}
    </div>
  );
}

function containerChoices(pod: KubernetesPodDetail): { name: string; group: string }[] {
  return [
    ...pod.containers.map((container) => ({ name: container.name, group: "容器" })),
    ...pod.init_containers.map((container) => ({ name: container.name, group: "初始化容器" })),
    ...pod.ephemeral_containers.map((container) => ({ name: container.name, group: "临时容器" })),
  ];
}

function TerminalSession({
  clusterId,
  clusterName,
  namespace,
  pod,
  canRecord,
  canReadRecordings,
  active,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  pod: KubernetesPodDetail;
  canRecord: boolean;
  canReadRecordings: boolean;
  active: boolean;
}) {
  const choices = containerChoices(pod);
  const [container, setContainer] = useState(choices[0]?.name ?? "");
  const [state, setState] = useState<ConnectionState>("idle");
  const [exit, setExit] = useState<ExitNotice | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [recordOutput, setRecordOutput] = useState(false);
  const [showRecordings, setShowRecordings] = useState(false);
  const [replayId, setReplayId] = useState<string | null>(null);
  const [replayNonce, setReplayNonce] = useState(0);
  const [recordingNotice, setRecordingNotice] = useState<string | null>(null);
  const createSession = useCreateTerminalSession();
  const queryClient = useQueryClient();
  const recordings = useTerminalRecordings(
    clusterId,
    namespace,
    pod.name,
    canReadRecordings && showRecordings,
  );
  const recording = useTerminalRecording(clusterId, namespace, pod.name, replayId);
  const ticketKey = useSubmissionKey(confirming);

  const surfaceRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const socketRef = useRef<WebSocket | null>(null);
  const replayTimerRef = useRef<number | null>(null);
  const refitRef = useRef<(() => void) | null>(null);
  const replayAttemptRef = useRef(0);
  // Invalidates a pending ticket request and all handlers belonging to an old
  // socket. In particular, a request resolving after this view unmounts must
  // not create a shell nobody can see or close.
  const connectionAttemptRef = useRef(0);

  const send = useCallback((message: PodExecClientMessage) => {
    const socket = socketRef.current;
    if (socket?.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify(message));
    }
  }, []);

  // The terminal is created once and outlives individual connections: a session
  // that ends must leave its scrollback readable rather than blank the view.
  useEffect(() => {
    const surface = surfaceRef.current;
    if (!surface) {
      return;
    }
    const terminal = new Terminal({
      convertEol: false,
      cursorBlink: true,
      fontFamily:
        'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace',
      fontSize: 12,
      scrollback: 5_000,
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
        // A view being torn down can report a zero-sized box; nothing to fit.
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

    const inputSubscription = terminal.onData((data) => {
      for (const chunk of encodeTerminalInput(data)) {
        send({ type: "stdin", data: chunk });
      }
    });
    // The Server forwards this to the container's TTY, so the shell redraws at
    // the size the operator actually has.
    const resizeSubscription = terminal.onResize(({ cols, rows }) =>
      send({ type: "resize", columns: cols, rows }),
    );

    return () => {
      cancelAnimationFrame(fitFrame);
      cancelAnimationFrame(refreshFrame);
      resizeObserver.disconnect();
      visibilityObserver.disconnect();
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      refitRef.current = null;
      inputSubscription.dispose();
      resizeSubscription.dispose();
      terminal.dispose();
      terminalRef.current = null;
      fitRef.current = null;
    };
  }, [send]);

  useEffect(() => {
    if (active) refitRef.current?.();
  }, [active]);

  // Nothing must outlive the view: an abandoned socket keeps a shell running in
  // the container.
  useEffect(() => {
    return () => {
      connectionAttemptRef.current += 1;
      socketRef.current?.close(1000, "view closed");
      socketRef.current = null;
      replayAttemptRef.current += 1;
      if (replayTimerRef.current !== null) {
        window.clearTimeout(replayTimerRef.current);
      }
    };
  }, []);

  const disconnect = () => {
    connectionAttemptRef.current += 1;
    socketRef.current?.close(1000, "closed by operator");
    socketRef.current = null;
    replayAttemptRef.current += 1;
    if (replayTimerRef.current !== null) {
      window.clearTimeout(replayTimerRef.current);
      replayTimerRef.current = null;
    }
    setState("closed");
  };

  const connect = () => {
    const terminal = terminalRef.current;
    if (!terminal || !container) {
      return;
    }
    fitRef.current?.fit();
    setExit(null);
    setRecordingNotice(null);
    setState("connecting");
    const attempt = connectionAttemptRef.current + 1;
    connectionAttemptRef.current = attempt;
    void createSession
      .mutateAsync({
        clusterId,
        namespace,
        podName: pod.name,
        uid: pod.uid,
        container,
        columns: terminal.cols,
        rows: terminal.rows,
        idempotencyKey: ticketKey,
        recordOutput,
      })
      .then((session) => {
        if (connectionAttemptRef.current !== attempt) {
          return;
        }
        const socket = new WebSocket(terminalSocketUrl(session.websocket_path), [
          session.subprotocol || POD_EXEC_SUBPROTOCOL,
        ]);
        socketRef.current = socket;
        setConfirming(false);

        socket.onopen = () => {
          if (connectionAttemptRef.current !== attempt) {
            socket.close(1000, "superseded connection");
            return;
          }
          setState("open");
          terminal.focus();
          // The ticket carried the size at request time; this covers a resize
          // between minting it and the socket opening.
          send({ type: "resize", columns: terminal.cols, rows: terminal.rows });
        };
        socket.onmessage = (event) => {
          if (connectionAttemptRef.current !== attempt) {
            return;
          }
          let message: PodExecServerMessage;
          try {
            message = JSON.parse(event.data as string) as PodExecServerMessage;
          } catch {
            return;
          }
          if (message.type === "exit") {
            setExit(exitNotice(message));
            return;
          }
          if (message.type === "recording") {
            setRecordingNotice(
              message.recording_saved
                ? `终端输出已保存为记录 ${message.recording_id}`
                : "终端输出录制保存失败，在线会话不受影响",
            );
            if (message.recording_saved) {
              void queryClient.invalidateQueries({
                queryKey: queryKeys.podTerminalRecordings(clusterId, namespace, pod.name),
              });
            }
            return;
          }
          if (message.data) {
            terminal.write(decodeTerminalOutput(message.data));
          }
        };
        socket.onclose = () => {
          if (connectionAttemptRef.current !== attempt) {
            return;
          }
          if (socketRef.current === socket) {
            socketRef.current = null;
          }
          setState("closed");
        };
        socket.onerror = () => {
          if (connectionAttemptRef.current === attempt) {
            setState("closed");
          }
        };
      })
      .catch(() => {
        if (connectionAttemptRef.current === attempt) {
          setState("closed");
        }
      });
  };

  useEffect(() => {
    const item = recording.data;
    const terminal = terminalRef.current;
    if (!item || !terminal || replayId === null) {
      return;
    }
    replayAttemptRef.current += 1;
    const attempt = replayAttemptRef.current;
    terminal.reset();
    setExit(null);
    setState("replaying");
    const frames = item.frames ?? [];
    const started = performance.now();
    let index = 0;
    const writeNext = () => {
      if (replayAttemptRef.current !== attempt) {
        return;
      }
      if (index >= frames.length) {
        replayTimerRef.current = null;
        setState("closed");
        setExit({ text: `[ZKE] 回放结束 · ${item.result}`, failed: item.result === "failed" });
        return;
      }
      const frame = frames[index];
      if (!frame) {
        setState("closed");
        return;
      }
      const delay = Math.max(0, frame.offset_ms - (performance.now() - started));
      replayTimerRef.current = window.setTimeout(() => {
        terminal.write(decodeTerminalOutput(frame.data));
        index += 1;
        writeNext();
      }, delay);
    };
    writeNext();
    return () => {
      if (replayAttemptRef.current === attempt) {
        replayAttemptRef.current += 1;
      }
      if (replayTimerRef.current !== null) {
        window.clearTimeout(replayTimerRef.current);
        replayTimerRef.current = null;
      }
    };
  }, [recording.data, replayId, replayNonce]);

  // The notice is written into the terminal itself, where the operator is
  // already looking, rather than into a banner above it.
  useEffect(() => {
    if (exit && terminalRef.current) {
      terminalRef.current.writeln("");
      // Written as escapes rather than raw control bytes: a literal ESC in a
      // source file is one stray editor away from disappearing.
      terminalRef.current.writeln(`\u001b[${exit.failed ? "31" : "90"}m${exit.text}\u001b[0m`);
    }
  }, [exit]);

  const grouped = choices.reduce<Record<string, string[]>>((accumulator, choice) => {
    (accumulator[choice.group] ??= []).push(choice.name);
    return accumulator;
  }, {});
  const connected = state === "open";
  const busy = connected || state === "connecting" || state === "replaying";

  return (
    <>
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <div className="grid content-start gap-1.5">
          <Label htmlFor="terminal-container">容器</Label>
          <Select value={container} onValueChange={setContainer} disabled={busy}>
            <SelectTrigger id="terminal-container" className="w-56">
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

        <div className="ml-auto flex items-center gap-2">
          {canReadRecordings ? (
            <Button
              size="sm"
              variant="secondary"
              disabled={connected || state === "connecting"}
              onClick={() => setShowRecordings((value) => !value)}
            >
              <History />
              会话记录
            </Button>
          ) : null}
          {connected || state === "replaying" ? (
            <Button size="sm" variant="secondary" className="text-danger" onClick={disconnect}>
              <Square />
              {state === "replaying" ? "停止回放" : "断开"}
            </Button>
          ) : (
            <Button
              size="sm"
              variant="primary"
              disabled={!container || state === "connecting"}
              onClick={() => setConfirming(true)}
            >
              <Play />
              {state === "closed" ? "重新连接" : "连接"}
            </Button>
          )}
        </div>
      </div>

      {showRecordings ? (
        <div className="border-border bg-muted/30 mb-3 max-h-44 overflow-auto rounded-lg border p-2">
          {recordings.isLoading ? (
            <span className="text-subtle-foreground text-xs">正在读取会话记录…</span>
          ) : recordings.error ? (
            <Alert tone="danger">{errorMessage(recordings.error)}</Alert>
          ) : recordings.data?.length ? (
            <div className="grid gap-2">
              {recordings.data.map((item) => (
                <div
                  key={item.id}
                  className="border-border bg-background flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-xs"
                >
                  <span className="font-medium">{item.container}</span>
                  <span className="text-subtle-foreground">{formatAbsolute(item.started_at)}</span>
                  <Badge tone={item.result === "failed" ? "danger" : "neutral"}>
                    {item.result}
                  </Badge>
                  {item.truncated ? <Badge tone="warning">录制已截断</Badge> : null}
                  <Button
                    size="sm"
                    variant="ghost"
                    className="ml-auto"
                    disabled={busy || recording.isFetching}
                    onClick={() => {
                      setReplayId(item.id);
                      setReplayNonce((value) => value + 1);
                    }}
                  >
                    <Play />
                    回放
                  </Button>
                </div>
              ))}
            </div>
          ) : (
            <span className="text-subtle-foreground text-xs">暂无尚未过期的终端输出录制。</span>
          )}
        </div>
      ) : null}

      {createSession.error ? (
        <Alert tone="danger" className="mb-3">
          {errorMessage(createSession.error)}
        </Alert>
      ) : null}
      {recording.error ? (
        <Alert tone="danger" className="mb-3">
          {errorMessage(recording.error)}
        </Alert>
      ) : null}
      {recordingNotice ? (
        <Alert tone="info" className="mb-3">
          {recordingNotice}
        </Alert>
      ) : null}

      {/* The terminal owns its own scrolling and its own dark surface: a shell
          is read against the colours the programs inside it assume. */}
      <div className="border-border rounded-panel min-h-0 flex-1 overflow-hidden border bg-[#0b0d12] p-2">
        {/*
         * The element the terminal is opened into carries no padding and no
         * border, and the frame lives on the wrapper instead.
         *
         * The fit addon sizes the terminal from `getComputedStyle(parent).height`,
         * and with `box-sizing: border-box` that is the border box — the parent's
         * own padding and border included. Measured against this panel directly
         * it read 18px more than it had, which is more than one 14px row: the
         * bottom line was laid out past the edge and clipped away, and the same
         * 18px on the width made `cols` too large, so the shell wrapped its
         * lines at a width the operator did not have.
         */}
        <div ref={surfaceRef} className="h-full w-full" />
      </div>

      <div className="text-subtle-foreground mt-3 flex flex-wrap items-center gap-3 text-xs">
        <ConnectionBadge state={state} />
        {exit ? <span>{exit.text}</span> : null}
        <span>会话有最长时长与空闲超时；离开本页即断开。</span>
      </div>

      <SensitiveActionDialog
        open={confirming}
        onOpenChange={(open) => {
          if (createSession.isPending && !open) {
            return;
          }
          setConfirming(open);
        }}
        title="打开 Pod 终端"
        description="终端会在目标容器中启动一个交互式 Shell。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "Pod", name: pod.name, id: pod.uid },
          { label: "容器", name: container },
        ]}
        impacts={[
          "将在该容器中启动交互式 Shell，命令以容器自身的身份和权限执行。",
          recordOutput
            ? "将录制终端 stdout/stderr（可能包含敏感输出）并保留 7 天；stdin、Cookie 与票据始终不录制。"
            : "Server 只记录会话的发起者、目标和结果，终端输入输出不写入审计或录制。",
          "票据一次性且很快过期，会话另有最长时长和空闲超时。",
        ]}
        confirmLabel="打开终端"
        destructive
        pending={createSession.isPending}
        error={createSession.error}
        onConfirm={connect}
      >
        {canRecord ? (
          <label className="flex items-start gap-2 text-sm">
            <Checkbox
              checked={recordOutput}
              onCheckedChange={(checked) => setRecordOutput(checked === true)}
            />
            <span>录制本次终端输出（不录制键盘输入，记录 7 天后过期）</span>
          </label>
        ) : null}
      </SensitiveActionDialog>
    </>
  );
}

function exitNotice(message: Extract<PodExecServerMessage, { type: "exit" }>): ExitNotice {
  const failed =
    (message.result !== undefined && message.result !== "ok") ||
    (message.exit_code !== undefined && message.exit_code !== 0);
  const parts = [message.message || message.reason || "会话已结束"];
  if (message.exit_code !== undefined && message.exit_code !== 0) {
    parts.push(`退出码 ${message.exit_code}`);
  }
  if (message.output_limit_reached) {
    parts.push("已达到输出上限");
  }
  return { text: `[ZKE] ${parts.join(" · ")}`, failed };
}

function ConnectionBadge({ state }: { state: ConnectionState }) {
  if (state === "open") {
    return (
      <Badge tone="success">
        <StatusDot tone="success" />
        已连接
      </Badge>
    );
  }
  if (state === "connecting") {
    return (
      <Badge tone="info">
        <StatusDot tone="info" />
        连接中
      </Badge>
    );
  }
  if (state === "replaying") {
    return (
      <Badge tone="info">
        <StatusDot tone="info" />
        回放中
      </Badge>
    );
  }
  return (
    <Badge tone="neutral">
      <StatusDot tone="neutral" />
      {state === "closed" ? "已断开" : "未连接"}
    </Badge>
  );
}
