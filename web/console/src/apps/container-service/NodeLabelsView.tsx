import { useState } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { useNode, useUpdateNodeLabels } from "@/api/queries/nodes";
import type { KubernetesNodeDetail } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert } from "@/components/ui/misc";
import { useSubmissionKey } from "@/lib/use-submission-key";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const LABEL_NAME = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/;
const LABEL_VALUE = /^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$/;
const MAX_DNS_LABEL_LENGTH = 63;
const MAX_SUBDOMAIN_LENGTH = 253;
const MAX_LABEL_VALUE_LENGTH = 63;
/**
 * A bound on one edit rather than on the Node: Kubernetes does not cap how many
 * labels an object carries, but a page of hundreds of text inputs is no longer
 * an editor, and a Node that already carries more than this can still be read
 * in the detail view and written through YAML.
 */
const MAX_LABELS = 100;

/**
 * Labels kubelet and the cloud controller set on every heartbeat.
 *
 * Editing one is allowed — Kubernetes allows it, and refusing here would mean
 * the YAML editor is the only way out — but it is reported before it is
 * submitted, because the next node status update will simply put the original
 * value back and the operator would be left looking for who reverted them.
 */
const RECONCILED_LABEL_KEYS = new Set([
  "kubernetes.io/hostname",
  "kubernetes.io/os",
  "kubernetes.io/arch",
  "beta.kubernetes.io/os",
  "beta.kubernetes.io/arch",
  "node.kubernetes.io/instance-type",
  "beta.kubernetes.io/instance-type",
  "topology.kubernetes.io/region",
  "topology.kubernetes.io/zone",
  "failure-domain.beta.kubernetes.io/region",
  "failure-domain.beta.kubernetes.io/zone",
]);

type LabelDraft = { key: string; value: string };

/** What one edit does to the Node's labels, keyed the way it will be sent. */
type LabelChanges = {
  added: string[];
  changed: string[];
  removed: string[];
  /** Only the touched keys; `null` removes one. */
  patch: Record<string, string | null>;
};

function labelChanges(baseline: Record<string, string>, rows: LabelDraft[]): LabelChanges {
  const result: LabelChanges = { added: [], changed: [], removed: [], patch: {} };
  const kept = new Set<string>();
  for (const row of rows) {
    const key = row.key.trim();
    if (key === "") {
      continue;
    }
    kept.add(key);
    const value = row.value.trim();
    if (!(key in baseline)) {
      result.added.push(key);
      result.patch[key] = value;
    } else if (baseline[key] !== value) {
      result.changed.push(key);
      result.patch[key] = value;
    }
  }
  for (const key of Object.keys(baseline)) {
    if (!kept.has(key)) {
      result.removed.push(key);
      result.patch[key] = null;
    }
  }
  return result;
}

function labelsProblem(rows: LabelDraft[]): string | null {
  if (rows.length > MAX_LABELS) {
    return `一次最多编辑 ${MAX_LABELS} 个标签。`;
  }
  const seen = new Set<string>();
  for (const [index, row] of rows.entries()) {
    const key = row.key.trim();
    const where = `第 ${index + 1} 项`;
    if (key === "") {
      return `${where}缺少标签键。`;
    }
    if (!qualifiedName(key)) {
      return `${where}的键「${key}」不是合法的 Kubernetes 标签键：可选的 DNS 子域名前缀加 /，名称部分最长 ${MAX_DNS_LABEL_LENGTH} 个字符的字母、数字、-、_ 或 .。`;
    }
    if (seen.has(key)) {
      return `标签键「${key}」重复。`;
    }
    seen.add(key);
    const value = row.value.trim();
    if (!LABEL_VALUE.test(value) || value.length > MAX_LABEL_VALUE_LENGTH) {
      return `标签「${key}」的值必须是最长 ${MAX_LABEL_VALUE_LENGTH} 个字符的字母、数字、-、_ 或 .，可以为空。`;
    }
  }
  return null;
}

function qualifiedName(value: string): boolean {
  const slash = value.indexOf("/");
  if (slash === -1) {
    return LABEL_NAME.test(value) && value.length <= MAX_DNS_LABEL_LENGTH;
  }
  const prefix = value.slice(0, slash);
  const name = value.slice(slash + 1);
  return (
    DNS_SUBDOMAIN.test(prefix) &&
    prefix.length <= MAX_SUBDOMAIN_LENGTH &&
    LABEL_NAME.test(name) &&
    name.length <= MAX_DNS_LABEL_LENGTH
  );
}

/**
 * Edits the labels of one Node.
 *
 * A page rather than a dialog: the number of fields is the number of labels the
 * Node already carries, which on a real Node is well past the two or three a
 * dialog is for, and reading a scheduling key wrong is how a workload ends up
 * unschedulable.
 */
export function NodeLabelsView({
  clusterId,
  clusterName,
  name,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  name: string;
  onBack: () => void;
}) {
  const detail = useNode(clusterId, name);

  if (detail.error) {
    return (
      <>
        <PageHeader title={`标签 · ${name}`} onBack={onBack} />
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      </>
    );
  }
  if (detail.isLoading || !detail.data) {
    return (
      <>
        <PageHeader title={`标签 · ${name}`} onBack={onBack} />
        <LoadingState />
      </>
    );
  }
  return (
    <NodeLabelsEditor
      clusterId={clusterId}
      clusterName={clusterName}
      node={detail.data}
      onBack={onBack}
    />
  );
}

function NodeLabelsEditor({
  clusterId,
  clusterName,
  node,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  node: KubernetesNodeDetail;
  onBack: () => void;
}) {
  const update = useUpdateNodeLabels();
  const [previewed, setPreviewed] = useState(false);
  const previewKey = useSubmissionKey(!previewed);
  const applyKey = useSubmissionKey(previewed);

  // Pinned at mount so the baseline the diff is taken against is the one the
  // rows were filled from. A background refetch arriving mid-edit must not turn
  // a key the operator never touched into a change they never asked for.
  const [baseline] = useState<Record<string, string>>(() => ({ ...node.labels }));
  const [rows, setRows] = useState<LabelDraft[]>(() =>
    Object.entries(baseline)
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([key, value]) => ({ key, value })),
  );

  const problem = labelsProblem(rows);
  const changes = labelChanges(baseline, rows);
  const touched = Object.keys(changes.patch);
  const reconciled = touched.filter((key) => RECONCILED_LABEL_KEYS.has(key));
  const submittable = problem === null && touched.length > 0;

  const submit = (dryRun: boolean) => {
    void update
      .mutateAsync({
        clusterId,
        name: node.name,
        labels: changes.patch,
        dryRun,
        idempotencyKey: dryRun ? previewKey : applyKey,
      })
      .then(() => {
        if (dryRun) {
          setPreviewed(true);
          return;
        }
        toast.success(`节点 ${node.name} 的标签已更新`);
        onBack();
      })
      .catch(() => undefined);
  };

  const updateRow = (index: number, patch: Partial<LabelDraft>) =>
    setRows(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <>
      <div className="grid gap-3">
        <PageHeader title={`标签 · ${node.name}`} onBack={onBack} backDisabled={update.isPending} />

        <div className="max-w-4xl">
          <div className="mb-2 flex flex-wrap items-baseline gap-2">
            <h4 className="text-foreground text-[13px] font-medium">标签</h4>
            <span className="text-subtle-foreground text-xs">
              键可带 DNS 子域名前缀，例如 node-role.kubernetes.io/worker；值可以为空
            </span>
          </div>
          {problem ? (
            <Alert tone="warning" className="mb-2">
              {problem}
            </Alert>
          ) : null}
          <div className="grid gap-2">
            {rows.map((row, index) => (
              // Keyed by position: a row's identity here is where it sits, and
              // keying by the label key would remount the input being typed in.
              <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
                <div className="grid gap-2 @md:grid-cols-2">
                  <Input
                    value={row.key}
                    aria-label={`第 ${index + 1} 个标签键`}
                    placeholder="标签键"
                    autoComplete="off"
                    spellCheck={false}
                    className="zke-mono text-xs"
                    onChange={(event) => updateRow(index, { key: event.target.value })}
                  />
                  <Input
                    value={row.value}
                    aria-label={`第 ${index + 1} 个标签值`}
                    placeholder="标签值"
                    autoComplete="off"
                    spellCheck={false}
                    className="zke-mono text-xs"
                    onChange={(event) => updateRow(index, { value: event.target.value })}
                  />
                </div>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={`移除第 ${index + 1} 个标签`}
                  onClick={() => setRows(rows.filter((_, position) => position !== index))}
                >
                  <X />
                </Button>
              </div>
            ))}
            <div>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => setRows([...rows, { key: "", value: "" }])}
              >
                <Plus />
                添加标签
              </Button>
            </div>
          </div>
        </div>

        {reconciled.length > 0 ? (
          <Alert tone="warning" className="max-w-4xl">
            以下标签由 kubelet 或云控制器在每次节点上报时写回，本次改动可能很快被覆盖：
            <span className="zke-mono"> {reconciled.join("、")}</span>
          </Alert>
        ) : null}
        {update.error ? (
          <Alert tone="danger" className="max-w-4xl">
            {errorMessage(update.error)}
          </Alert>
        ) : null}

        <div className="flex max-w-4xl flex-wrap items-center justify-end gap-3 pb-2">
          <LabelChangeSummary changes={changes} />
          <Button
            variant="primary"
            size="sm"
            disabled={!submittable || update.isPending}
            onClick={() => submit(true)}
          >
            {update.isPending ? "DryRun 预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed}
        onOpenChange={(open) => !open && setPreviewed(false)}
        title="确认更新节点标签"
        description="DryRun 预检已通过。确认后将向该集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "节点", name: node.name, id: node.uid },
        ]}
        impacts={[
          `本次提交 ${changeSummaryText(changes)}；未在本页改动的标签保持原样，不会被覆盖。`,
          "标签决定调度：nodeSelector、节点亲和性和拓扑分布约束都按标签匹配节点，移除正在被引用的标签会让相关 Pod 不再能调度到该节点。",
          "已经运行在该节点上的 Pod 不会因为标签变化被驱逐或重新调度。",
          ...(changes.added.some(isRoleLabel) ||
          changes.changed.some(isRoleLabel) ||
          changes.removed.some(isRoleLabel)
            ? ["角色标签发生变化，节点列表与详情中显示的角色会随之改变。"]
            : []),
          ...(reconciled.length > 0
            ? ["其中包含由 kubelet 或云控制器维护的标签，节点下一次上报时可能被改回。"]
            : []),
        ]}
        confirmLabel="确认更新"
        destructive={changes.removed.length > 0}
        pending={update.isPending}
        error={update.error}
        onConfirm={() => submit(false)}
      >
        <LabelChangeList changes={changes} baseline={baseline} />
      </SensitiveActionDialog>
    </>
  );
}

function isRoleLabel(key: string): boolean {
  return key.startsWith("node-role.kubernetes.io/") || key === "kubernetes.io/role";
}

function changeSummaryText(changes: LabelChanges): string {
  const parts: string[] = [];
  if (changes.added.length > 0) parts.push(`新增 ${changes.added.length} 项`);
  if (changes.changed.length > 0) parts.push(`修改 ${changes.changed.length} 项`);
  if (changes.removed.length > 0) parts.push(`移除 ${changes.removed.length} 项`);
  return parts.length === 0 ? "没有改动" : parts.join("、");
}

function LabelChangeSummary({ changes }: { changes: LabelChanges }) {
  const total = changes.added.length + changes.changed.length + changes.removed.length;
  if (total === 0) {
    return <span className="text-subtle-foreground text-xs">没有改动</span>;
  }
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {changes.added.length > 0 ? <Badge tone="success">新增 {changes.added.length}</Badge> : null}
      {changes.changed.length > 0 ? <Badge tone="info">修改 {changes.changed.length}</Badge> : null}
      {changes.removed.length > 0 ? (
        <Badge tone="danger">移除 {changes.removed.length}</Badge>
      ) : null}
    </div>
  );
}

/**
 * The exact keys about to be written, before and after.
 *
 * The form above shows the result; this shows the difference. An operator who
 * renamed a key by typing over it is one keystroke away from removing the old
 * label and adding an unrelated new one, and the confirmation is the last place
 * that difference is still visible.
 */
function LabelChangeList({
  changes,
  baseline,
}: {
  changes: LabelChanges;
  baseline: Record<string, string>;
}) {
  return (
    <div className="grid max-h-48 gap-1 overflow-y-auto">
      {changes.added.map((key) => (
        <LabelChangeRow
          key={`add/${key}`}
          tone="success"
          label="新增"
          text={`${key}=${changes.patch[key] ?? ""}`}
        />
      ))}
      {changes.changed.map((key) => (
        <LabelChangeRow
          key={`set/${key}`}
          tone="info"
          label="修改"
          text={`${key}：${baseline[key] || "（空）"} → ${changes.patch[key] || "（空）"}`}
        />
      ))}
      {changes.removed.map((key) => (
        <LabelChangeRow
          key={`remove/${key}`}
          tone="danger"
          label="移除"
          text={`${key}=${baseline[key] ?? ""}`}
        />
      ))}
    </div>
  );
}

function LabelChangeRow({
  tone,
  label,
  text,
}: {
  tone: "success" | "info" | "danger";
  label: string;
  text: string;
}) {
  return (
    <div className="flex items-start gap-2 text-xs">
      <Badge tone={tone} className="shrink-0">
        {label}
      </Badge>
      <span className="zke-mono text-foreground min-w-0 break-all">{text}</span>
    </div>
  );
}
