import { useState } from "react";
import { ExternalLink, Link2 } from "lucide-react";

import { errorCode, errorMessage } from "@/api/errors";
import { useCreatePodAccessSession } from "@/api/queries/pod-access";
import { usePod } from "@/api/queries/pods";
import { PageHeader } from "@/apps/AppShell";
import { SecretReveal } from "@/components/common/secret-reveal";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { NumericInput } from "@/components/ui/input";
import { FieldError, Label } from "@/components/ui/label";
import { Alert, CardTitle } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSubmissionKey } from "@/lib/use-submission-key";
import { formatAbsolute } from "@/lib/time";

export function PodAccessView({
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
    <div className="grid gap-3">
      <PageHeader title={`${podName} · Pod 访问`} onBack={onBack} />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !detail.data ? (
        <LoadingState />
      ) : detail.data.uid !== podUid ? (
        // Danger rather than the warning its sibling views use, and the
        // difference is the point: everywhere else a stale UID means the page is
        // describing an object that no longer exists, here it would mean opening
        // a port on a Pod nobody asked for.
        <Alert tone="danger">
          该 Pod 已被同名重新创建，本次入口绑定的 UID 已失效。请返回列表重新打开。
        </Alert>
      ) : (
        <PodAccessForm
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

function PodAccessForm({
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
  const [port, setPort] = useState("");
  const [sessionDuration, setSessionDuration] = useState("900");
  const [confirming, setConfirming] = useState(false);
  const [replacing, setReplacing] = useState(false);
  const createSession = useCreatePodAccessSession();
  const submissionKey = useSubmissionKey(confirming || replacing);
  const portNumber = Number(port);
  const sessionDurationSeconds = parseSessionDuration(sessionDuration);
  const validPort = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65_535;
  const ticket = createSession.data;

  const create = (replaceExisting: boolean) => {
    void createSession
      .mutateAsync({
        clusterId,
        namespace,
        podName,
        uid: podUid,
        port: portNumber,
        sessionDurationSeconds,
        replaceExisting,
        idempotencyKey: submissionKey,
      })
      .then(() => {
        setConfirming(false);
        setReplacing(false);
      })
      .catch((error: unknown) => {
        if (!replaceExisting && errorCode(error) === "pod_access_already_active") {
          createSession.reset();
          setConfirming(false);
          setReplacing(true);
        }
      });
  };

  return (
    /*
     * No padding and no scroll region of its own.
     *
     * The shell's work area already provides both — `p-4` and `overflow-auto` —
     * and this view was adding `px-5 pb-5` on top of them, which is why it was
     * the one page in 容器服务 whose content started 36px from the window edge
     * while every other page started at 16px. The extra `overflow-auto` also put
     * a second scrollbar inset from the window edge instead of on it. A short
     * form has nothing to scroll independently of the page it is on.
     */
    <div className="grid gap-4">
      <Alert tone="info">
        创建一个位于独立 Pod Access 地址的临时入口。浏览器对该地址的根路径、静态资源、流式响应和
        WebSocket 请求会转发到 Pod；ZKE API 和登录 Cookie 不会转发。该能力仅适用于 HTTP 服务，不支持
        Redis、MySQL、SSH 等非 HTTP 协议。
      </Alert>

      <div className="border-border rounded-panel flex flex-wrap items-end gap-3 border p-4">
        <div className="grid gap-1.5">
          <Label htmlFor="pod-access-port">Pod HTTP 端口</Label>
          <NumericInput
            id="pod-access-port"
            className="w-40"
            placeholder="1–65535"
            required
            maxLength={5}
            value={port}
            aria-invalid={port !== "" && !validPort ? true : undefined}
            aria-describedby={!validPort && port !== "" ? "pod-access-port-error" : undefined}
            onValueChange={(value) => {
              setPort(value);
              createSession.reset();
              setReplacing(false);
            }}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="pod-access-duration">访问时长</Label>
          <Select
            value={sessionDuration}
            onValueChange={(value) => {
              setSessionDuration(value);
              createSession.reset();
              setReplacing(false);
            }}
          >
            <SelectTrigger id="pod-access-duration" className="w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="900">15 分钟</SelectItem>
              <SelectItem value="1800">30 分钟</SelectItem>
              <SelectItem value="3600">1 小时</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <Button
          variant="primary"
          disabled={!validPort || createSession.isPending}
          onClick={() => setConfirming(true)}
        >
          <Link2 />
          {createSession.isPending ? "创建中…" : ticket ? "重新创建地址" : "创建访问地址"}
        </Button>
        {!validPort && port !== "" ? (
          <FieldError id="pod-access-port-error" className="basis-full">
            端口必须是 1–65535 之间的整数。
          </FieldError>
        ) : null}
      </div>

      {createSession.error ? (
        <Alert tone="danger">{errorMessage(createSession.error)}</Alert>
      ) : null}

      {ticket ? (
        <section className="border-border bg-surface rounded-panel grid gap-3 border p-4">
          <div>
            <CardTitle>一次性激活地址</CardTitle>
            <p className="text-subtle-foreground mt-1 text-xs">
              地址需在 {formatAbsolute(ticket.activation_expires_at)}{" "}
              前首次打开；激活后访问会话最长持续{" "}
              {formatSessionDuration(ticket.session_expires_in_seconds)}。
            </p>
          </div>

          {/*
           * Masked, like every other one-time credential the product hands out.
           *
           * The copy under it already said the address is equivalent to a
           * temporary credential, and it was then printed in the clear in a
           * read-only field — on a page an operator is as likely to be screen
           * sharing as not. Masking costs nothing here because neither of the
           * two things anyone does with it needs the value on screen: 复制 puts
           * it on the clipboard and 打开 navigates to it.
           */}
          <SecretReveal
            label="Pod 访问地址"
            value={ticket.access_url}
            actions={
              <Button size="sm" variant="secondary" asChild>
                <a href={ticket.access_url} target="_blank" rel="noopener noreferrer">
                  <ExternalLink />
                  新窗口打开
                </a>
              </Button>
            }
            warning={
              <>
                该地址绑定当前登录会话，等同临时凭证，请勿分享。激活地址只能使用一次；打开后请保留新窗口，再次打开同一地址会提示失效，需要在这里重新创建。同一个
                Pod 同时只保留一个待激活地址或访问会话，再次创建时会先要求明确替换。不同 Pod
                需要在同一浏览器中切换入口时，激活页也会要求确认。
              </>
            }
          />
        </section>
      ) : null}

      <SensitiveActionDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="确认创建 Pod 访问地址"
        description="该操作会允许浏览器在限定时间内访问 Pod 中指定端口的 HTTP 服务。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "Pod", name: podName, id: podUid },
          { label: "远端端口", name: String(portNumber) },
          { label: "访问时长", name: formatSessionDuration(sessionDurationSeconds) },
        ]}
        impacts={[
          "地址绑定当前登录 Session、Pod UID 和单个端口，首次打开后失效。",
          "同一个 Pod 同时只保留一个待激活地址或访问会话；已有入口时会再次要求明确替换。",
          "访问权限会被周期复核；退出登录、权限收回、Pod 被替换或到期后连接会关闭。",
          "请求与响应正文不会写入日志或审计，但访问地址持有人可使用创建者获准的临时入口。",
        ]}
        confirmLabel="创建访问地址"
        pending={createSession.isPending}
        error={createSession.error}
        onConfirm={() => create(false)}
      />

      <SensitiveActionDialog
        open={replacing}
        onOpenChange={setReplacing}
        destructive
        title="替换已有 Pod 访问入口"
        description="同一个 Pod 已有待激活地址或访问会话。继续会立即使旧地址和现有代理连接失效。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "Pod", name: podName, id: podUid },
          { label: "远端端口", name: String(portNumber) },
          { label: "新访问时长", name: formatSessionDuration(sessionDurationSeconds) },
        ]}
        impacts={[
          "旧的待激活地址会立即失效，不能再次使用。",
          "已激活的旧入口及其 HTTP、流式响应或 WebSocket 连接会立即关闭。",
          "替换只针对当前集群中的这个 Pod UID，不会影响其他 Pod。",
          "新地址仍需在短期内首次打开，之后按所选时长和流量上限运行。",
        ]}
        confirmLabel="替换并创建新地址"
        pending={createSession.isPending}
        error={createSession.error}
        onConfirm={() => create(true)}
      />
    </div>
  );
}

function formatSessionDuration(seconds: number): string {
  return seconds === 3600 ? "1 小时" : `${seconds / 60} 分钟`;
}

function parseSessionDuration(value: string): 900 | 1800 | 3600 {
  switch (value) {
    case "1800":
      return 1800;
    case "3600":
      return 3600;
    default:
      return 900;
  }
}
