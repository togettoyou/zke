import { useId, useState } from "react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import {
  useCreatePolicyResource,
  useDeletePolicyResource,
  usePolicyResources,
  useUpdatePolicyResource,
} from "@/api/queries/policies";
import type { KubernetesPolicyResourceSummary } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert } from "@/components/ui/misc";
import { displayToQuantity, quantityToDisplay } from "@/lib/quantity";
import { useSubmissionKey } from "@/lib/use-submission-key";

import {
  isUnmodelledQuotaKey,
  NAMESPACE_QUOTA_FIELDS,
  NAMESPACE_QUOTA_GROUPS,
  NAMESPACE_QUOTA_OBJECT_NAME,
  type QuotaField,
} from "./namespace-quota-catalog";

/** A Namespace holds a handful of quotas at most; this is a ceiling, not a page. */
const QUOTA_LIST_LIMIT = 50;

/**
 * The quota of one Namespace, as one form.
 *
 * Kubernetes enforces every ResourceQuota in a Namespace at once, and a quota
 * may be narrowed to a scope, so "the quota of this Namespace" is only a single
 * editable object in the common case: exactly one quota, covering everything.
 * The other shapes are shown as they are and sent to 策略管理, where each object
 * is edited on its own terms — a flat form over several quotas would have to
 * invent which one an entered number belongs to.
 */
export function NamespaceQuotaView({
  clusterId,
  clusterName,
  namespace,
  canCreate,
  canUpdate,
  canDelete,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  /** The Namespace whose quota is being managed. */
  namespace: string;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
  onBack: () => void;
}) {
  /*
   * One request, and it has to be the list.
   *
   * The object cannot be fetched by name, because its name is not known: the
   * Namespace may hold no quota, one under any name an operator or kubectl gave
   * it, or several. `zke-namespace-quota` is only what this page calls the one
   * it creates itself, so asking for that name directly would report "no quota"
   * for every Namespace whose quota came from somewhere else. The list answers
   * how many there are, what the one is called, and — since the summary carries
   * hard, used, scopes and scopeSelector — everything the form then edits.
   */
  const quotas = usePolicyResources(clusterId, namespace, "resourcequotas", {
    limit: QUOTA_LIST_LIMIT,
  });
  const objects = quotas.data?.resources ?? [];
  const single = objects.length === 1 ? objects[0] : undefined;
  // A quota narrowed by scope or scopeSelector counts a subset of the Namespace
  // rather than all of it, so its `used` is not the Namespace's usage and the
  // flat form would misread it.
  const quota = single?.resource_quota;
  const scoped = Boolean(quota && (quota.scopes.length > 0 || quota.scope_selector.length > 0));

  return (
    <div className="grid gap-3">
      <PageHeader
        title={`配额管理 · ${namespace}`}
        onBack={onBack}
        actions={
          <RefreshAction isFetching={quotas.isFetching} onRefresh={() => void quotas.refetch()} />
        }
      />

      {quotas.error ? (
        <ErrorState error={quotas.error} onRetry={() => void quotas.refetch()} />
      ) : quotas.isLoading ? (
        <LoadingState />
      ) : objects.length > 1 ? (
        <UneditableQuotas
          objects={objects}
          reason={`${namespace} 中有 ${objects.length} 个 ResourceQuota。Kubernetes 会同时执行它们，实际生效的是每一项中最严格的那个，因此一份合并后的表单无法说明某个数字属于哪个对象。`}
        />
      ) : single && scoped ? (
        <UneditableQuotas
          objects={objects}
          reason="该 ResourceQuota 限定了 scope 或 scopeSelector，只统计命名空间中符合条件的一部分对象。它的已用量不是整个命名空间的用量，因此不在这里编辑。"
        />
      ) : (
        <QuotaEditor
          /*
           * resourceVersion is part of the key, not just the UID. The editor
           * pins identity at mount, and a successful write invalidates this
           * list; without the remount the form would keep the version it just
           * consumed and the next save would be refused as a conflict. A
           * refetch only produces a new version when the object actually
           * changed, so an open form is not disturbed by one that did not.
           */
          key={single ? `${single.uid}/${single.resource_version}` : "new"}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          existing={single ?? null}
          canCreate={canCreate}
          canUpdate={canUpdate}
          canDelete={canDelete}
        />
      )}
    </div>
  );
}

/** Read-only rendering for the quota shapes this form does not edit. */
function UneditableQuotas({
  objects,
  reason,
}: {
  objects: KubernetesPolicyResourceSummary[];
  reason: string;
}) {
  return (
    <div className="grid gap-3">
      <Alert tone="warning">
        {reason}
        <span className="mt-1 block">请在「策略管理 → ResourceQuota」中按对象查看和编辑。</span>
      </Alert>
      {objects.map((object) => (
        <section key={object.uid} className="border-border bg-surface rounded-panel border">
          <div className="border-border flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b px-3 py-2">
            <span className="text-foreground text-[13px] font-medium">{object.name}</span>
            {object.resource_quota?.scopes.length ? (
              <span className="text-muted-foreground text-xs">
                scope：{object.resource_quota.scopes.join("、")}
              </span>
            ) : null}
          </div>
          <dl className="grid gap-1.5 px-3 py-2 text-[13px]">
            {Object.entries(object.resource_quota?.hard ?? {}).map(([key, limit]) => (
              <div key={key} className="flex items-baseline gap-2">
                <dt className="zke-mono text-muted-foreground min-w-0 flex-1 text-xs break-all">
                  {key}
                </dt>
                <dd className="zke-tnum text-foreground shrink-0">
                  {object.resource_quota?.used[key] ?? "—"} / {limit}
                </dd>
              </div>
            ))}
          </dl>
        </section>
      ))}
    </div>
  );
}

/** What the form is about to do, decided by whether anything is left limited. */
type QuotaOperation = "create" | "update" | "remove" | "none";

function QuotaEditor({
  clusterId,
  clusterName,
  namespace,
  existing,
  canCreate,
  canUpdate,
  canDelete,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  existing: KubernetesPolicyResourceSummary | null;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
}) {
  const create = useCreatePolicyResource();
  const update = useUpdatePolicyResource();
  const remove = useDeletePolicyResource();
  const [previewed, setPreviewed] = useState(false);
  const previewKey = useSubmissionKey(!previewed);
  const applyKey = useSubmissionKey(previewed);

  const hard = existing?.resource_quota?.hard ?? {};
  const used = existing?.resource_quota?.used ?? {};

  /*
   * Identity is pinned when the editor mounts, not read again at submit time.
   * The Server checks UID and resourceVersion against a fresh read, so taking a
   * newer resourceVersion than the numbers copied into this form would turn a
   * conflict the Server should refuse into a silent overwrite of whatever
   * someone else tightened in the meantime.
   */
  const [pinned] = useState(() =>
    existing
      ? { name: existing.name, uid: existing.uid, resourceVersion: existing.resource_version }
      : null,
  );
  // The value each field started at, kept so an untouched field can be submitted
  // as the exact string Kubernetes returned. 1000Mi is not a round number of Gi,
  // and rewriting it as one would edit a limit nobody asked to change.
  const [initial] = useState(() => displayValues(hard));
  const [draft, setDraft] = useState(() => displayValues(hard));
  // Kubernetes accepts quota keys this form has no row for — GPUs, ingresses,
  // any `count/<resource>.<group>`. An update replaces the whole `hard` map, so
  // they are carried through instead of being dropped by an edit that never
  // mentioned them.
  const [preserved] = useState(() =>
    Object.fromEntries(Object.entries(hard).filter(([key]) => isUnmodelledQuotaKey(key))),
  );

  const invalid = NAMESPACE_QUOTA_FIELDS.filter((field) => {
    const value = (draft[field.key] ?? "").trim();
    return value !== "" && displayToQuantity(value, field.unit) === null;
  });

  const nextHard = buildHard(draft, initial, hard, preserved);
  const operation: QuotaOperation = existing
    ? Object.keys(nextHard).length === 0
      ? "remove"
      : "update"
    : Object.keys(nextHard).length === 0
      ? "none"
      : "create";
  const permitted =
    operation === "create" ? canCreate : operation === "update" ? canUpdate : canDelete;
  const readOnly = existing ? !canUpdate && !canDelete : !canCreate;
  const mutation = operation === "create" ? create : operation === "remove" ? remove : update;
  const dirty = NAMESPACE_QUOTA_FIELDS.some(
    (field) => (draft[field.key] ?? "").trim() !== (initial[field.key] ?? ""),
  );

  const submit = (dryRun: boolean) => {
    const shared = {
      clusterId,
      namespace,
      resource: "resourcequotas" as const,
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    const request =
      operation === "create"
        ? create.mutateAsync({
            ...shared,
            name: NAMESPACE_QUOTA_OBJECT_NAME,
            spec: { resource_quota: { hard: nextHard } },
          })
        : operation === "update"
          ? update.mutateAsync({
              ...shared,
              name: pinned?.name as string,
              uid: pinned?.uid as string,
              resourceVersion: pinned?.resourceVersion as string,
              spec: { resource_quota: { hard: nextHard } },
            })
          : remove.mutateAsync({
              ...shared,
              name: pinned?.name as string,
              uid: pinned?.uid as string,
              resourceVersion: pinned?.resourceVersion as string,
            });
    void request
      .then(() => {
        if (dryRun) {
          setPreviewed(true);
          return;
        }
        setPreviewed(false);
        toast.success(
          operation === "remove" ? `${namespace} 的配额限制已移除` : `${namespace} 的配额已保存`,
        );
      })
      .catch(() => undefined);
  };

  return (
    <>
      <div className="grid gap-3">
        {existing ? null : (
          <Alert tone="info">
            {namespace}{" "}
            尚未设置任何配额限制，命名空间中的对象只受集群自身的容量约束。填写任意一项后保存，
            将创建名为 <span className="zke-mono">{NAMESPACE_QUOTA_OBJECT_NAME}</span> 的
            ResourceQuota。
          </Alert>
        )}
        {readOnly ? (
          <Alert tone="warning">当前身份没有修改该集群资源的权限，本页只读。</Alert>
        ) : null}
        {Object.keys(preserved).length > 0 ? (
          <Alert tone="info">
            该 ResourceQuota 还包含本表单未建模的配额项（
            <span className="zke-mono">{Object.keys(preserved).join("、")}</span>
            ），保存时按当前值原样保留；需要修改请使用「策略管理 → ResourceQuota」或 YAML。
          </Alert>
        ) : null}

        {NAMESPACE_QUOTA_GROUPS.map((group) => (
          <section key={group.title} className="border-border bg-surface rounded-panel border">
            <div className="border-border flex items-center justify-between gap-3 border-b px-3 py-2">
              <h4 className="text-foreground text-[13px] font-medium">{group.title}</h4>
              <span className="text-subtle-foreground text-xs">配额累计使用量 / 总配额</span>
            </div>
            <div>
              {group.fields.map((field) => (
                <QuotaRow
                  key={field.key}
                  field={field}
                  value={draft[field.key] ?? ""}
                  used={used[field.key]}
                  invalid={invalid.includes(field)}
                  disabled={readOnly}
                  onChange={(value) => setDraft((rows) => ({ ...rows, [field.key]: value }))}
                />
              ))}
            </div>
          </section>
        ))}

        {invalid.length > 0 ? (
          <Alert tone="danger">
            以下配额项不是合法数值：{invalid.map((field) => field.label).join("、")}
            。CPU 与容量可以带小数，数量必须是整数。
          </Alert>
        ) : null}
        {operation === "remove" ? (
          <Alert tone="warning">
            所有配额项都已清空。保存将删除 ResourceQuota{" "}
            <span className="zke-mono">{pinned?.name}</span>，该命名空间将不再受配额限制。
          </Alert>
        ) : null}
        {mutation.error ? <Alert tone="danger">{errorMessage(mutation.error)}</Alert> : null}

        <div className="flex items-center justify-end gap-3">
          <span className="text-subtle-foreground text-xs">留空表示不限制</span>
          <Button
            variant="primary"
            size="sm"
            disabled={
              readOnly ||
              !permitted ||
              !dirty ||
              invalid.length > 0 ||
              operation === "none" ||
              mutation.isPending
            }
            onClick={() => submit(true)}
          >
            {mutation.isPending ? "预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed}
        onOpenChange={(open) => !open && setPreviewed(false)}
        title={
          operation === "create"
            ? "确认创建命名空间配额"
            : operation === "remove"
              ? "确认移除命名空间配额"
              : "确认更新命名空间配额"
        }
        description="DryRun 已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          {
            label: "ResourceQuota",
            name: pinned?.name ?? NAMESPACE_QUOTA_OBJECT_NAME,
            id: pinned?.uid,
          },
        ]}
        impacts={impactsFor(operation, namespace)}
        confirmLabel={
          operation === "create" ? "确认创建" : operation === "remove" ? "确认删除" : "确认更新"
        }
        confirmationText={operation === "remove" ? pinned?.name : undefined}
        destructive={operation !== "create"}
        pending={mutation.isPending}
        error={mutation.error}
        onConfirm={() => submit(false)}
      />
    </>
  );
}

function QuotaRow({
  field,
  value,
  used,
  invalid,
  disabled,
  onChange,
}: {
  field: QuotaField;
  value: string;
  /** Kubernetes only reports usage for keys the quota actually limits. */
  used: string | undefined;
  invalid: boolean;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  const id = useId();
  const usedDisplay = used === undefined ? "—" : (quantityToDisplay(used, field.unit) ?? used);

  return (
    <div className="border-border/60 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 border-b px-3 py-2 last:border-b-0">
      <label htmlFor={id} className="text-foreground text-[13px]">
        {field.label}
      </label>
      <div className="flex items-center gap-2">
        <span className="zke-tnum text-subtle-foreground w-20 text-right text-xs">
          {usedDisplay}
        </span>
        <span className="text-subtle-foreground text-xs">/</span>
        <Input
          id={id}
          value={value}
          disabled={disabled}
          inputMode="decimal"
          autoComplete="off"
          spellCheck={false}
          placeholder="不限制"
          aria-invalid={invalid || undefined}
          aria-label={`${field.label} 总配额`}
          className="zke-tnum h-8 w-32 text-right text-[13px]"
          onChange={(event) => onChange(event.target.value)}
        />
        <span className="text-muted-foreground w-6 text-xs">{field.unitLabel}</span>
      </div>
    </div>
  );
}

/** Every modelled field's current limit, in the unit its row is labelled with. */
function displayValues(hard: Record<string, string>): Record<string, string> {
  return Object.fromEntries(
    NAMESPACE_QUOTA_FIELDS.map((field) => {
      const limit = hard[field.key];
      if (limit === undefined) {
        return [field.key, ""];
      }
      return [field.key, quantityToDisplay(limit, field.unit) ?? limit];
    }),
  );
}

/**
 * The `hard` map to submit.
 *
 * A field left as it was submits the string Kubernetes returned, so an edit to
 * one limit cannot restate the others in a different unit. An empty field is a
 * key that is absent, which is what "not limited" means in a ResourceQuota —
 * there is no value meaning unlimited.
 */
function buildHard(
  draft: Record<string, string>,
  initial: Record<string, string>,
  hard: Record<string, string>,
  preserved: Record<string, string>,
): Record<string, string> {
  const result: Record<string, string> = { ...preserved };
  for (const field of NAMESPACE_QUOTA_FIELDS) {
    const value = (draft[field.key] ?? "").trim();
    if (value === "") {
      continue;
    }
    const original = hard[field.key];
    if (original !== undefined && value === (initial[field.key] ?? "")) {
      result[field.key] = original;
      continue;
    }
    const quantity = displayToQuantity(value, field.unit);
    if (quantity !== null) {
      result[field.key] = quantity;
    }
  }
  return result;
}

function impactsFor(operation: QuotaOperation, namespace: string): string[] {
  const shared = [
    "配额由 Kubernetes 准入控制执行：超出限制的新对象会被拒绝创建，已经存在的对象不会被删除或收缩。",
  ];
  switch (operation) {
    case "create":
      return [
        `将在 ${namespace} 中创建一个 ResourceQuota，此后该命名空间的用量必须留在所填限额内。`,
        "为 CPU 或内存设置配额后，该命名空间中新建的容器必须显式声明对应的 requests/limits，否则会被拒绝。",
        ...shared,
      ];
    case "update":
      return [
        "本表单建模的配额项会整体替换现有配置：本次留空的项将不再受限制。",
        "本表单未建模的配额项按当前值原样保留。",
        "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，更新会被拒绝而不是覆盖。",
        ...shared,
      ];
    case "remove":
      return [
        `将删除该 ResourceQuota，${namespace} 中的对象此后不再受配额限制。`,
        "已经运行的工作负载不受影响；此前因为配额被拒绝的创建请求将重新被允许。",
        "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，删除会被拒绝。",
      ];
    case "none":
      return [];
  }
}
