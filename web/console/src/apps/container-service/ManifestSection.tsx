import { useMemo, useRef, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileUp, Play, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { newIdempotencyKey } from "@/api/client";
import { errorMessage } from "@/api/errors";
import {
  MAX_MANIFEST_BYTES,
  useSubmitManifest,
  type ManifestDocument,
  type ManifestOperation,
  type ManifestResult,
} from "@/api/queries/manifests";
import { useNamespaces } from "@/api/queries/namespaces";
import { SectionToolbarActions } from "@/apps/AppShell";
import { DataTable } from "@/components/common/data-table";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { YamlEditor } from "@/components/common/yaml-editor";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

/**
 * Only the target Cluster. Unlike the other sections this one is not scoped by
 * the shell's Namespace picker: the Namespace it takes is a value submitted with
 * the request, not the scope of a list, so it owns that control itself.
 */
type ManifestSectionProps = {
  clusterId: string;
  clusterName: string;
};

/** One page at the endpoint's maximum, as the shell's own picker reads it. */
const NAMESPACE_PICKER_LIMIT = 500;

/** What the operator is preparing. The two share every step but the last. */
const OPERATIONS: { id: ManifestOperation; label: string }[] = [
  { id: "apply", label: "应用（apply）" },
  { id: "delete", label: "删除（delete）" },
];

const ACTION_LABELS: Record<ManifestDocument["action"], string> = {
  create: "创建",
  update: "更新",
  delete: "删除",
  absent: "对象不存在",
  unknown: "无法判定",
};

const STATUS_LABELS: Record<ManifestDocument["status"], string> = {
  planned: "预检通过",
  refused: "权限不足",
  invalid: "文档无效",
  succeeded: "成功",
  skipped: "已跳过",
  failed: "失败",
  not_attempted: "未执行",
};

/**
 * The colour says how much attention a row needs, and the text says what
 * happened. Nothing here is carried by colour alone.
 */
const STATUS_TONES: Record<ManifestDocument["status"], string> = {
  planned: "text-success",
  refused: "text-danger",
  invalid: "text-danger",
  succeeded: "text-success",
  skipped: "text-muted-foreground",
  failed: "text-danger",
  not_attempted: "text-warning",
};

/**
 * A document the dry run could not submit reads as 未预检, not 预检通过.
 *
 * The difference is the whole point: one is the API Server saying yes, the other
 * is nothing having been asked. Shown in the neutral colour rather than the
 * success one, because it is neither a pass nor a problem.
 */
function statusLabel(document: ManifestDocument): { label: string; tone: string } {
  if (document.status === "planned" && !document.previewed) {
    return { label: "未预检", tone: "text-muted-foreground" };
  }
  return { label: STATUS_LABELS[document.status], tone: STATUS_TONES[document.status] };
}

/**
 * Applies and deletes whole YAML manifests, the way `kubectl apply -f` and
 * `kubectl delete -f` do.
 *
 * Its own category rather than a button inside 资源对象浏览器: what it writes is
 * not one resource type but any of them, so it belongs beside that section
 * rather than inside it — the browser is the read-side escape hatch for types
 * the typed categories do not model, and this is the write-side one.
 *
 * The flow is the platform's usual one for a sensitive operation — 预检, then
 * confirm, then execute — with one addition that only a manifest needs: the
 * preview is a table rather than a sentence, because a file holds many objects
 * and "what is about to happen" is a different answer for each of them.
 */
export function ManifestSection({ clusterId, clusterName }: ManifestSectionProps) {
  const namespaces = useNamespaces(clusterId, { limit: NAMESPACE_PICKER_LIMIT });
  const namespaceNames = (namespaces.data?.namespaces ?? []).map((item) => item.name);
  const [operation, setOperation] = useState<ManifestOperation>("apply");
  const [source, setSource] = useState<"paste" | "upload">("paste");
  const [manifest, setManifest] = useState("");
  const [namespace, setNamespace] = useState("");
  const [force, setForce] = useState(false);
  const [fileNames, setFileNames] = useState<string[]>([]);
  const [fileError, setFileError] = useState<string | null>(null);
  // The plan carries the manifest that produced it, rather than only the result.
  // Confirming a plan made from an older document would confirm something other
  // than what runs, and the check has to survive an edit made while the dry run
  // was still in flight.
  const [plan, setPlan] = useState<{ manifest: string; result: ManifestResult } | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [outcome, setOutcome] = useState<ManifestResult | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const submit = useSubmitManifest();

  // A failed attempt must reuse its key when the same manifest is retried, but a
  // corrected file is a new request and must not collide with the old request
  // fingerprint under that key. Refs rather than state: nothing renders from
  // them, and they are only ever read inside the two submit handlers.
  const planAttempt = useRef<{ manifest: string; key: string } | null>(null);
  const executeAttempt = useRef<{ manifest: string; key: string } | null>(null);

  const oversized = new Blob([manifest]).size > MAX_MANIFEST_BYTES;
  const empty = manifest.trim() === "";
  const currentPlan = plan?.manifest === manifest ? (plan?.result ?? null) : null;
  const destructive = operation === "delete";

  const reset = () => {
    setPlan(null);
    setOutcome(null);
    submit.reset();
  };

  const onManifestChange = (next: string) => {
    setManifest(next);
    reset();
  };

  const readFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) {
      return;
    }
    const names: string[] = [];
    const contents: string[] = [];
    let total = 0;
    for (const file of Array.from(files)) {
      total += file.size;
      if (total > MAX_MANIFEST_BYTES) {
        setFileError(`所选文件合计超过 4 MiB，服务端不会接受。`);
        return;
      }
      names.push(file.name);
      contents.push(await file.text());
    }
    setFileError(null);
    setFileNames(names);
    // Joined with a document separator so several files read as one manifest.
    // A file that already ends with one gets a harmless empty document, which
    // the Server skips.
    onManifestChange(contents.join("\n---\n"));
  };

  const runPlan = () => {
    const planned = manifest;
    if (planAttempt.current?.manifest !== planned) {
      planAttempt.current = { manifest: planned, key: newIdempotencyKey() };
    }
    setOutcome(null);
    void submit
      .mutateAsync({
        clusterId,
        manifest: planned,
        namespace: namespace || undefined,
        operation,
        dryRun: true,
        force,
        idempotencyKey: planAttempt.current.key,
      })
      .then((result) => setPlan({ manifest: planned, result }))
      .catch(() => setPlan(null));
  };

  const execute = () => {
    if (executeAttempt.current?.manifest !== manifest) {
      executeAttempt.current = { manifest, key: newIdempotencyKey() };
    }
    void submit
      .mutateAsync({
        clusterId,
        manifest,
        namespace: namespace || undefined,
        operation,
        dryRun: false,
        force,
        idempotencyKey: executeAttempt.current.key,
      })
      .then((result) => {
        setConfirming(false);
        setOutcome(result);
        setPlan(null);
        // A new execution of the same file is a new request: the objects have
        // moved on, and reusing the key would replay this result.
        executeAttempt.current = null;
        planAttempt.current = null;
        if (result.failed) {
          toast.error("清单执行中断，请查看逐条结果");
          return;
        }
        toast.success(destructive ? "清单删除已完成" : "清单应用已完成");
      })
      .catch(() => undefined);
  };

  const shown = outcome ?? currentPlan;
  const columns = useMemo(() => manifestColumns(), []);
  const counts = useMemo(() => summarise(shown), [shown]);
  const unpreviewed = useMemo(
    () =>
      shown?.dry_run
        ? shown.documents.filter((item) => item.status === "planned" && !item.previewed).length
        : 0,
    [shown],
  );

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <SectionToolbarActions>
        <Tabs
          value={operation}
          onValueChange={(value) => {
            setOperation(value as ManifestOperation);
            reset();
          }}
        >
          <TabsList>
            {OPERATIONS.map((item) => (
              <TabsTrigger key={item.id} value={item.id}>
                {item.label}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      </SectionToolbarActions>

      {/*
        The Namespace picker is here rather than in the shell toolbar: it is not
        the scope of a list, it is a value submitted with the request — what
        `kubectl -n` supplies for documents that name no Namespace of their own.
        Documents that name one keep it, and one that names a different one is
        refused rather than moved.
      */}
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-muted-foreground text-xs">默认命名空间</span>
        <Select
          value={namespace || "__none__"}
          onValueChange={(value) => setNamespace(value === "__none__" ? "" : value)}
        >
          <SelectTrigger className="w-56" aria-label="默认命名空间">
            <SelectValue placeholder="不填充" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__none__">不填充</SelectItem>
            {namespaceNames.map((name) => (
              <SelectItem key={name} value={name}>
                {name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Tabs value={source} onValueChange={(value) => setSource(value as "paste" | "upload")}>
          <TabsList>
            <TabsTrigger value="paste">手动输入</TabsTrigger>
            <TabsTrigger value="upload">上传文件</TabsTrigger>
          </TabsList>
        </Tabs>
        {source === "upload" ? (
          <>
            <input
              ref={fileInput}
              type="file"
              multiple
              accept=".yaml,.yml,application/yaml,text/yaml"
              className="hidden"
              onChange={(event) => {
                void readFiles(event.target.files);
                // Cleared so selecting the same file again still fires.
                event.target.value = "";
              }}
            />
            <Button size="sm" variant="secondary" onClick={() => fileInput.current?.click()}>
              <FileUp />
              选择 YAML 文件
            </Button>
            {fileNames.length > 0 ? (
              <span className="text-subtle-foreground text-xs">
                已读取 {fileNames.length} 个文件：{fileNames.join("、")}
              </span>
            ) : null}
          </>
        ) : null}

        <div className="ml-auto flex items-center gap-2">
          {operation === "apply" ? (
            <label className="text-muted-foreground flex items-center gap-1.5 text-xs">
              <input
                type="checkbox"
                checked={force}
                onChange={(event) => {
                  setForce(event.target.checked);
                  reset();
                }}
              />
              强制接管字段所有权
            </label>
          ) : null}
          <Button
            size="sm"
            variant={destructive ? "secondary" : "primary"}
            disabled={empty || oversized || submit.isPending}
            onClick={runPlan}
          >
            {destructive ? <Trash2 /> : <Play />}
            {submit.isPending && !confirming ? "预检中…" : "执行预检"}
          </Button>
          <Button
            size="sm"
            variant="primary"
            disabled={
              currentPlan === null ||
              !currentPlan.allowed ||
              !currentPlan.valid ||
              currentPlan.failed ||
              submit.isPending
            }
            onClick={() => setConfirming(true)}
          >
            {destructive ? "确认删除" : "确认应用"}
          </Button>
        </div>
      </div>

      {oversized ? (
        <Alert tone="danger">清单超过 4 MiB，服务端不会接受；请拆分后再提交。</Alert>
      ) : null}
      {fileError ? <Alert tone="danger">{fileError}</Alert> : null}
      {submit.error ? <Alert tone="danger">{errorMessage(submit.error)}</Alert> : null}
      {shown?.catalog_partial ? (
        <Alert tone="warning">
          集群 API 发现不完整，「集群未提供该 Kind」只表示本次没查到，不代表集群中没有该类型。
        </Alert>
      ) : null}
      {outcome?.failed ? (
        <Alert tone="danger">
          执行在第一个失败的文档处停止。Kubernetes 没有事务，已写入的对象不会回滚——
          请按下表逐条核对「成功」「失败」和「未执行」，修正后重新提交。
        </Alert>
      ) : null}
      {/*
        Four different things a preview can say, and they must not be collapsed:
        a document ZKE cannot send, a document the permissions do not cover, a
        document Kubernetes rejected, and a preview that passed. Reporting 预检通过
        while the result carried a failure told the operator the opposite of what
        the disabled 确认 button was doing.
      */}
      {shown && !outcome ? (
        !shown.valid ? (
          <Alert tone="danger">
            清单中存在无法解析成请求的文档，整份清单会被拒绝，不会写入任何对象——
            请按下表的「说明」修正后重新预检。
          </Alert>
        ) : !shown.allowed ? (
          <Alert tone="danger">
            清单中存在当前身份无权处理的文档，整份清单会被拒绝，不会写入任何对象。
          </Alert>
        ) : shown.failed ? (
          <Alert tone="danger">
            预检未通过：有文档被集群拒绝，请按下表的「说明」修正后重新预检。
            排在其后的文档未参与预检。
          </Alert>
        ) : (
          <Alert tone="info">
            预检通过：{counts}。确认后按文档{destructive ? "反序" : "顺序"}执行。
            {unpreviewed > 0
              ? `其中 ${unpreviewed} 个文档位于本清单将要创建的命名空间中，服务端无法预先校验——` +
                "它们的「未预检」不代表有问题，只代表没有可校验的对象。"
              : ""}
          </Alert>
        )
      ) : null}

      <YamlEditor
        value={manifest}
        onChange={onManifestChange}
        readOnly={submit.isPending}
        label="Kubernetes YAML 清单"
        className="min-h-[12rem] flex-1"
      />

      {shown ? (
        <div className="min-h-0 shrink-0" style={{ maxHeight: "40%" }}>
          <DataTable
            columns={columns}
            data={shown.documents}
            rowKey={(row) => String(row.index)}
            emptyTitle="清单中没有文档"
          />
        </div>
      ) : null}

      <SensitiveActionDialog
        open={confirming}
        onOpenChange={(open) => !open && setConfirming(false)}
        title={destructive ? "确认按清单删除对象" : "确认应用清单"}
        description="DryRun 已通过。确认后将向同一集群逐条提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(namespace ? [{ label: "默认命名空间", name: namespace }] : []),
          { label: "文档", name: counts },
        ]}
        impacts={manifestImpacts(operation, currentPlan, force)}
        // The target is a set of objects rather than one, so the Cluster's name
        // is what there is to type — and naming the Cluster is the check that
        // matters when one manifest can be applied to any of them.
        confirmationText={clusterName}
        confirmLabel={destructive ? "确认删除" : "确认应用"}
        destructive
        pending={submit.isPending}
        error={submit.error}
        onConfirm={execute}
      />
    </div>
  );
}

function summarise(result: ManifestResult | null): string {
  if (!result) {
    return "";
  }
  const byAction = new Map<string, number>();
  for (const document of result.documents) {
    const label = ACTION_LABELS[document.action];
    byAction.set(label, (byAction.get(label) ?? 0) + 1);
  }
  return Array.from(byAction, ([label, count]) => `${label} ${count}`).join("、");
}

function manifestImpacts(
  operation: ManifestOperation,
  plan: ManifestResult | null,
  force: boolean,
): string[] {
  const impacts = [
    "Kubernetes 没有事务：文档逐条执行，遇到第一个失败即停止，此前已写入的对象不会回滚。",
  ];
  if (operation === "delete") {
    impacts.push(
      "按文档反序删除，并携带预检时读到的 UID 与 resourceVersion；期间对象若被重建，该条会被拒绝而不是误删。",
      "删除会连带其受控对象，且不可撤销。",
    );
    return impacts;
  }
  impacts.push(
    "按文档顺序以 Server-Side Apply 提交：对象不存在则创建，存在则合并，清单中未出现的字段由 ZKE 接管后会被移除。",
    "控制器可能因此重建 Pod 或触发滚动更新。",
  );
  if (force) {
    impacts.push("已勾选强制接管：与其他 field manager 冲突的字段会被本次提交夺取所有权。");
  }
  if (plan?.documents.some((document) => document.action === "create")) {
    impacts.push("其中包含新建对象，创建后需要自行清理。");
  }
  return impacts;
}

function manifestColumns(): ColumnDef<ManifestDocument, unknown>[] {
  return [
    {
      id: "index",
      header: "#",
      size: 56,
      cell: ({ row }) => <span className="zke-tnum">{row.original.index + 1}</span>,
    },
    {
      id: "object",
      header: "对象",
      cell: ({ row }) => (
        <div className="flex flex-col">
          <span className="font-medium">
            {row.original.kind || "—"} / {row.original.name || "—"}
          </span>
          <span className="text-subtle-foreground zke-mono text-[11px]">
            {row.original.api_version || "—"}
            {row.original.namespace ? ` · ${row.original.namespace}` : ""}
          </span>
        </div>
      ),
    },
    {
      id: "action",
      header: "动作",
      size: 110,
      cell: ({ row }) => ACTION_LABELS[row.original.action],
    },
    {
      id: "status",
      header: "结果",
      size: 110,
      cell: ({ row }) => {
        const status = statusLabel(row.original);
        return <span className={status.tone}>{status.label}</span>;
      },
    },
    {
      id: "permission",
      header: "所需权限",
      size: 200,
      cell: ({ row }) => (
        <span className="zke-mono text-[11px]">{row.original.permission || "—"}</span>
      ),
    },
    {
      id: "message",
      header: "说明",
      cell: ({ row }) =>
        row.original.error_message ? (
          <span className="text-danger text-[12px]">{row.original.error_message}</span>
        ) : (
          <span className="text-subtle-foreground">—</span>
        ),
    },
  ];
}
