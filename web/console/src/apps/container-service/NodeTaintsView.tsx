import { useState } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { useGenericResource } from "@/api/queries/kubernetes-resources";
import { useUpdateNodeTaints, type NodeTaint } from "@/api/queries/nodes";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSubmissionKey } from "@/lib/use-submission-key";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const TAINT_NAME = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/;
const TAINT_VALUE = /^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$/;
const MAX_DNS_LABEL_LENGTH = 63;
const MAX_SUBDOMAIN_LENGTH = 253;
const MAX_TAINT_VALUE_LENGTH = 63;
/** A bound on one edit, for the reason the label editor has one. */
const MAX_TAINTS = 50;

const NODE_RESOURCE = { group: "", version: "v1", resource: "nodes" } as const;

const EFFECTS = [
  {
    value: "NoSchedule",
    label: "NoSchedule",
    description: "不再调度新的 Pod 到该节点，已运行的 Pod 不受影响。",
  },
  {
    value: "PreferNoSchedule",
    label: "PreferNoSchedule",
    description: "调度器尽量避开该节点，但在没有别的选择时仍会调度上去。",
  },
  {
    value: "NoExecute",
    label: "NoExecute",
    description: "不再调度新的 Pod，并驱逐该节点上不容忍这条污点的 Pod。",
  },
] as const;

/**
 * Taints Kubernetes itself sets and clears as a Node's condition changes.
 *
 * Editing one is allowed — Kubernetes allows it — but a value the node
 * lifecycle controller owns will simply be put back, and an operator who
 * removed one to "fix" a NotReady Node would be left looking for who reverted
 * them.
 */
const RECONCILED_TAINT_KEYS = new Set([
  "node.kubernetes.io/not-ready",
  "node.kubernetes.io/unreachable",
  "node.kubernetes.io/memory-pressure",
  "node.kubernetes.io/disk-pressure",
  "node.kubernetes.io/pid-pressure",
  "node.kubernetes.io/network-unavailable",
  "node.kubernetes.io/unschedulable",
  "node.cloudprovider.kubernetes.io/uninitialized",
  "node.cloudprovider.kubernetes.io/shutdown",
]);

type TaintDraft = { key: string; value: string; effect: string };

function taintIdentity(taint: { key: string; effect: string }): string {
  return `${taint.key}:${taint.effect}`;
}

function draftsFrom(taints: NodeTaint[]): TaintDraft[] {
  return taints
    .map((taint) => ({ key: taint.key, value: taint.value ?? "", effect: taint.effect }))
    .sort((left, right) => taintIdentity(left).localeCompare(taintIdentity(right)));
}

/**
 * What the edit stores, in the shape Kubernetes stores it.
 *
 * `value` is omitted when empty and `timeAdded` is carried over from the object
 * that was read: it is set by the node lifecycle controller for NoExecute
 * taints, and rewriting the list without it would look like the taint had just
 * been added again.
 */
function storedTaints(rows: TaintDraft[], baseline: NodeTaint[]): NodeTaint[] {
  const previous = new Map(baseline.map((taint) => [taintIdentity(taint), taint]));
  return rows
    .filter((row) => row.key.trim() !== "")
    .map((row) => {
      const key = row.key.trim();
      const value = row.value.trim();
      const effect = row.effect;
      const carried = previous.get(taintIdentity({ key, effect }));
      return {
        key,
        ...(value === "" ? {} : { value }),
        effect,
        ...(carried?.timeAdded && carried.value === (value === "" ? undefined : value)
          ? { timeAdded: carried.timeAdded }
          : {}),
      };
    });
}

type TaintChanges = {
  added: TaintDraft[];
  changed: { before: NodeTaint; after: TaintDraft }[];
  removed: NodeTaint[];
};

function taintChanges(baseline: NodeTaint[], rows: TaintDraft[]): TaintChanges {
  const previous = new Map(baseline.map((taint) => [taintIdentity(taint), taint]));
  const result: TaintChanges = { added: [], changed: [], removed: [] };
  const kept = new Set<string>();
  for (const row of rows) {
    const key = row.key.trim();
    if (key === "") {
      continue;
    }
    const draft: TaintDraft = { key, value: row.value.trim(), effect: row.effect };
    const identity = taintIdentity(draft);
    kept.add(identity);
    const before = previous.get(identity);
    if (!before) {
      result.added.push(draft);
    } else if ((before.value ?? "") !== draft.value) {
      result.changed.push({ before, after: draft });
    }
  }
  for (const taint of baseline) {
    if (!kept.has(taintIdentity(taint))) {
      result.removed.push(taint);
    }
  }
  return result;
}

function taintsProblem(rows: TaintDraft[]): string | null {
  if (rows.length > MAX_TAINTS) {
    return `一次最多编辑 ${MAX_TAINTS} 条污点。`;
  }
  const seen = new Set<string>();
  for (const [index, row] of rows.entries()) {
    const key = row.key.trim();
    const where = `第 ${index + 1} 条`;
    if (key === "") {
      return `${where}缺少污点键。`;
    }
    if (!qualifiedName(key)) {
      return `${where}的键「${key}」不是合法的 Kubernetes 污点键：可选的 DNS 子域名前缀加 /，名称部分最长 ${MAX_DNS_LABEL_LENGTH} 个字符的字母、数字、-、_ 或 .。`;
    }
    if (!EFFECTS.some((effect) => effect.value === row.effect)) {
      return `${where}缺少效果。`;
    }
    // Kubernetes keys a taint by key and effect together, so the same key with
    // two different effects is two valid taints and only the pair has to be
    // unique.
    const identity = `${key}:${row.effect}`;
    if (seen.has(identity)) {
      return `污点「${key}」的 ${row.effect} 重复。`;
    }
    seen.add(identity);
    const value = row.value.trim();
    if (!TAINT_VALUE.test(value) || value.length > MAX_TAINT_VALUE_LENGTH) {
      return `污点「${key}」的值必须是最长 ${MAX_TAINT_VALUE_LENGTH} 个字符的字母、数字、-、_ 或 .，可以为空。`;
    }
  }
  return null;
}

function qualifiedName(value: string): boolean {
  const slash = value.indexOf("/");
  if (slash === -1) {
    return TAINT_NAME.test(value) && value.length <= MAX_DNS_LABEL_LENGTH;
  }
  const prefix = value.slice(0, slash);
  const name = value.slice(slash + 1);
  return (
    DNS_SUBDOMAIN.test(prefix) &&
    prefix.length <= MAX_SUBDOMAIN_LENGTH &&
    TAINT_NAME.test(name) &&
    name.length <= MAX_DNS_LABEL_LENGTH
  );
}

/**
 * Edits the taints of one Node.
 *
 * The counterpart to the label editor, and the more dangerous half of the pair:
 * a label only decides where a Pod may go, while a NoExecute taint evicts the
 * Pods already running that do not tolerate it.
 *
 * The Node is read through the generic resource endpoint rather than the typed
 * detail, because the write sends the taint list back with a JSON Patch `test`
 * against what was read. That comparison is byte-for-byte, so it has to be made
 * against the object Kubernetes stores rather than against a projection of it.
 */
export function NodeTaintsView({
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
  const node = useGenericResource(clusterId, NODE_RESOURCE, "", name);

  if (node.error) {
    return (
      <>
        <PageHeader title={`污点 · ${name}`} onBack={onBack} />
        <ErrorState error={node.error} onRetry={() => void node.refetch()} />
      </>
    );
  }
  if (node.isLoading || !node.data) {
    return (
      <>
        <PageHeader title={`污点 · ${name}`} onBack={onBack} />
        <LoadingState />
      </>
    );
  }
  const spec = (node.data.spec ?? {}) as { taints?: NodeTaint[] };
  return (
    <NodeTaintsEditor
      key={node.data.metadata?.resourceVersion ?? name}
      clusterId={clusterId}
      clusterName={clusterName}
      name={name}
      uid={node.data.metadata?.uid ?? ""}
      // `undefined` and `[]` are different objects here: one has no
      // `/spec/taints` path at all, the other has an empty list, and the patch
      // has to know which it is looking at.
      stored={Array.isArray(spec.taints) ? spec.taints : undefined}
      onBack={onBack}
    />
  );
}

function NodeTaintsEditor({
  clusterId,
  clusterName,
  name,
  uid,
  stored,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  name: string;
  uid: string;
  stored: NodeTaint[] | undefined;
  onBack: () => void;
}) {
  const update = useUpdateNodeTaints();
  const [previewed, setPreviewed] = useState(false);
  const previewKey = useSubmissionKey(!previewed);
  const applyKey = useSubmissionKey(previewed);

  const baseline = stored ?? [];
  const [rows, setRows] = useState<TaintDraft[]>(() => draftsFrom(baseline));

  const problem = taintsProblem(rows);
  const changes = taintChanges(baseline, rows);
  const total = changes.added.length + changes.changed.length + changes.removed.length;
  const touchedKeys = [
    ...changes.added.map((taint) => taint.key),
    ...changes.changed.map((change) => change.after.key),
    ...changes.removed.map((taint) => taint.key),
  ];
  const reconciled = [...new Set(touchedKeys.filter((key) => RECONCILED_TAINT_KEYS.has(key)))];
  const evicting = [...changes.added, ...changes.changed.map((change) => change.after)].filter(
    (taint) => taint.effect === "NoExecute",
  );
  const submittable = problem === null && total > 0;

  const submit = (dryRun: boolean) => {
    void update
      .mutateAsync({
        clusterId,
        name,
        uid,
        baseline: stored,
        taints: storedTaints(rows, baseline),
        dryRun,
        idempotencyKey: dryRun ? previewKey : applyKey,
      })
      .then(() => {
        if (dryRun) {
          setPreviewed(true);
          return;
        }
        toast.success(`节点 ${name} 的污点已更新`);
        onBack();
      })
      .catch(() => undefined);
  };

  const updateRow = (index: number, patch: Partial<TaintDraft>) =>
    setRows(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <>
      <div className="grid gap-3">
        <PageHeader title={`污点 · ${name}`} onBack={onBack} backDisabled={update.isPending} />

        <div className="max-w-4xl">
          <div className="mb-2 flex flex-wrap items-baseline gap-2">
            <h4 className="text-foreground text-[13px] font-medium">污点</h4>
            <span className="text-subtle-foreground text-xs">
              只有声明了对应容忍度（toleration）的 Pod
              才能调度到带污点的节点；同一个键可以有不同效果
            </span>
          </div>
          {problem ? (
            <Alert tone="warning" className="mb-2">
              {problem}
            </Alert>
          ) : null}
          <div className="grid gap-2">
            {rows.map((row, index) => (
              // Keyed by position, for the reason the label editor is: the row's
              // identity here is where it sits, not what has been typed into it.
              <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
                <div className="grid gap-2 @2xl:grid-cols-[1fr_1fr_180px]">
                  <Input
                    value={row.key}
                    aria-label={`第 ${index + 1} 条污点键`}
                    placeholder="污点键"
                    autoComplete="off"
                    spellCheck={false}
                    className="zke-mono text-xs"
                    onChange={(event) => updateRow(index, { key: event.target.value })}
                  />
                  <Input
                    value={row.value}
                    aria-label={`第 ${index + 1} 条污点值`}
                    placeholder="污点值（可为空）"
                    autoComplete="off"
                    spellCheck={false}
                    className="zke-mono text-xs"
                    onChange={(event) => updateRow(index, { value: event.target.value })}
                  />
                  <Select
                    value={row.effect}
                    onValueChange={(value) => updateRow(index, { effect: value })}
                  >
                    <SelectTrigger aria-label={`第 ${index + 1} 条污点效果`}>
                      <SelectValue placeholder="效果" />
                    </SelectTrigger>
                    <SelectContent>
                      {EFFECTS.map((effect) => (
                        <SelectItem key={effect.value} value={effect.value}>
                          {effect.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={`移除第 ${index + 1} 条污点`}
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
                onClick={() =>
                  setRows([...rows, { key: "", value: "", effect: "NoSchedule" as const }])
                }
              >
                <Plus />
                添加污点
              </Button>
            </div>
          </div>
          <dl className="text-subtle-foreground mt-3 grid gap-1 text-xs">
            {EFFECTS.map((effect) => (
              <div key={effect.value} className="flex gap-2">
                <dt className="zke-mono text-muted-foreground w-40 shrink-0">{effect.label}</dt>
                <dd>{effect.description}</dd>
              </div>
            ))}
          </dl>
        </div>

        {evicting.length > 0 ? (
          <Alert tone="danger" className="max-w-4xl">
            本次新增或修改了 NoExecute 污点，该节点上不容忍它的 Pod 会被立即驱逐。
          </Alert>
        ) : null}
        {reconciled.length > 0 ? (
          <Alert tone="warning" className="max-w-4xl">
            以下污点由 Kubernetes 自己按节点状况写入和清除，本次改动可能很快被覆盖：
            <span className="zke-mono"> {reconciled.join("、")}</span>
          </Alert>
        ) : null}
        {update.error ? (
          <Alert tone="danger" className="max-w-4xl">
            {errorMessage(update.error)}
          </Alert>
        ) : null}

        <div className="flex max-w-4xl flex-wrap items-center justify-end gap-3 pb-2">
          <TaintChangeSummary changes={changes} />
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
        title="确认更新节点污点"
        description="DryRun 预检已通过。确认后将向该集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "节点", name, id: uid },
        ]}
        impacts={[
          `本次提交 ${changeSummaryText(changes)}，写入的是整份污点列表。`,
          "提交时会校验污点列表仍与本页打开时一致；如果期间有人改过该节点的污点，本次提交会被拒绝，需要重新打开本页。",
          "污点决定调度：新调度的 Pod 必须声明匹配的容忍度才能落到该节点，移除正在生效的污点会让该节点重新接受普通工作负载。",
          ...(evicting.length > 0
            ? [
                `NoExecute 污点会驱逐已经在该节点上运行且不容忍它的 Pod：${evicting
                  .map((taint) => taint.key)
                  .join("、")}。`,
              ]
            : ["已经运行在该节点上的 Pod 不会因为本次改动被驱逐。"]),
          ...(reconciled.length > 0
            ? ["其中包含由 Kubernetes 自己维护的污点，节点状况变化时可能被改回。"]
            : []),
        ]}
        confirmLabel="确认更新"
        destructive={changes.removed.length > 0 || evicting.length > 0}
        pending={update.isPending}
        error={update.error}
        onConfirm={() => submit(false)}
      >
        <TaintChangeList changes={changes} />
      </SensitiveActionDialog>
    </>
  );
}

function taintText(taint: { key: string; value?: string; effect: string }): string {
  return `${taint.key}=${taint.value ?? ""}:${taint.effect}`;
}

function changeSummaryText(changes: TaintChanges): string {
  const parts: string[] = [];
  if (changes.added.length > 0) parts.push(`新增 ${changes.added.length} 条`);
  if (changes.changed.length > 0) parts.push(`修改 ${changes.changed.length} 条`);
  if (changes.removed.length > 0) parts.push(`移除 ${changes.removed.length} 条`);
  return parts.length === 0 ? "没有改动" : parts.join("、");
}

function TaintChangeSummary({ changes }: { changes: TaintChanges }) {
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

/** The exact taints about to be written, before and after. */
function TaintChangeList({ changes }: { changes: TaintChanges }) {
  return (
    <div className="grid max-h-48 gap-1 overflow-y-auto">
      {changes.added.map((taint) => (
        <TaintChangeRow
          key={`add/${taintIdentity(taint)}`}
          tone="success"
          label="新增"
          text={taintText(taint)}
        />
      ))}
      {changes.changed.map((change) => (
        <TaintChangeRow
          key={`set/${taintIdentity(change.after)}`}
          tone="info"
          label="修改"
          text={`${taintText(change.before)} → ${taintText(change.after)}`}
        />
      ))}
      {changes.removed.map((taint) => (
        <TaintChangeRow
          key={`remove/${taintIdentity(taint)}`}
          tone="danger"
          label="移除"
          text={taintText(taint)}
        />
      ))}
    </div>
  );
}

function TaintChangeRow({
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
