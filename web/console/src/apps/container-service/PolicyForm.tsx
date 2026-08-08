import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import {
  useCreatePolicyResource,
  usePolicyResource,
  useUpdatePolicyResource,
  type PolicyCreateSpec,
  type PolicyUpdateSpec,
} from "@/api/queries/policies";
import type {
  KubernetesLimitRangeItem,
  KubernetesNetworkPolicyPeer,
  KubernetesNetworkPolicyPort,
  KubernetesNetworkPolicyRule,
  KubernetesPolicyResource,
  KubernetesPolicyResourceDetail,
  KubernetesPolicyResourceSummary,
} from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { Input, NumericInput } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, Checkbox } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSubmissionKey } from "@/lib/use-submission-key";

import {
  LIMIT_RANGE_TYPES,
  NETWORK_PROTOCOLS,
  QUOTA_RESOURCE_SUGGESTIONS,
  QUOTA_SCOPES,
  policyKindLabel,
  type LimitRangeType,
  type QuotaScope,
} from "./policy-catalog";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
/** A Kubernetes quantity: a number with an optional binary or decimal suffix. */
const QUANTITY = /^\d+(\.\d+)?(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E|m)?$/;
/** A disruption budget bound: a count of Pods, or a percentage of them. */
const BUDGET_VALUE = /^\d+%?$/;
const DEFAULT_OPTION = "__default__";

type PairDraft = { key: string; value: string };

/**
 * The one thing currently blocking submission, and where it can be fixed.
 *
 * `section` is the title the section is rendered with rather than a key: the five
 * kinds render different sections, and one of them — LimitRange — renders a
 * numbered section per item, which no fixed set of keys can name. Every producer
 * and consumer of a title goes through the constants and helper below, so a title
 * cannot drift out of step with the section that shows it.
 */
type PolicyProblem = { section: string; message: string };

const SECTIONS = {
  basic: "基本信息",
  quotaHard: "额度",
  quotaScopes: "作用范围",
  networkTarget: "作用对象",
  networkDirections: "策略方向",
  networkIngress: "入站规则",
  networkEgress: "出站规则",
  budgetSelector: "保护的 Pod",
  budget: "预算",
  priority: "优先级",
} as const;

function limitItemSection(index: number): string {
  return `限制项 ${index + 1}`;
}

function policyNameProblem(name: string): PolicyProblem | null {
  const trimmed = name.trim();
  if (trimmed === "") {
    return at(SECTIONS.basic, "请填写名称。");
  }
  if (trimmed.length > 253) {
    return at(SECTIONS.basic, "名称最长 253 个字符。");
  }
  if (!DNS_SUBDOMAIN.test(trimmed)) {
    return at(
      SECTIONS.basic,
      "名称必须是合法的 DNS 子域名：只能包含小写字母、数字、连字符和点，并以字母或数字开头和结尾。",
    );
  }
  return null;
}

/*
 * A key/value list where a quantity is expected, checked once for every list of
 * them in this form: quotas, and the five groups of every LimitRange item.
 *
 * A row carrying a value under a blank key is reported rather than dropped. The
 * builders filter those out, so submitting one silently discards what was typed —
 * the operator sees a limit they entered simply not be there afterwards.
 */
function quantityPairsProblem(
  section: string,
  rows: PairDraft[],
  label: string,
): PolicyProblem | null {
  const seen = new Set<string>();
  for (const [index, row] of rows.entries()) {
    const key = row.key.trim();
    const value = row.value.trim();
    if (key === "") {
      if (value !== "") {
        return at(section, `${label}的第 ${index + 1} 项填了取值但没有资源名。`);
      }
      continue;
    }
    if (seen.has(key)) {
      return at(section, `${label}中的「${key}」重复，同一项只能出现一次。`);
    }
    seen.add(key);
    if (value === "") {
      return at(section, `${label}中的「${key}」缺少取值。`);
    }
    if (!QUANTITY.test(value)) {
      return at(section, `${label}中「${key}」的取值必须是 Kubernetes quantity，例如 10 或 20Gi。`);
    }
  }
  return null;
}

function at(section: string, message: string): PolicyProblem {
  return { section, message };
}

/**
 * Creates or edits one policy object.
 *
 * Create and edit share one form because for four of the five kinds they are
 * the same statement: the Server replaces the managed spec wholesale, so an
 * edit has to show the whole policy rather than a field of it — a quota shown
 * one line at a time is a quota nobody can read. PriorityClass is the
 * exception, and it says so: Kubernetes freezes its value at creation.
 */
export function PolicyForm({
  clusterId,
  clusterName,
  namespace,
  resource,
  target,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesPolicyResource;
  /** The object being edited, or `null` to create a new one. */
  target: KubernetesPolicyResourceSummary | null;
  onClose: () => void;
}) {
  // An edit is built on the full object rather than the list row: the list
  // carries what a table can show, and submitting a spec assembled from that
  // would drop everything it left out.
  const detail = usePolicyResource(clusterId, namespace, resource, target?.name ?? null);

  if (!target) {
    return (
      <PolicyFormBody
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        resource={resource}
        target={null}
        detail={null}
        onClose={onClose}
      />
    );
  }

  const title = `编辑 ${policyKindLabel(resource)} · ${target.name}`;
  if (detail.error) {
    return (
      <>
        <PageHeader title={title} onBack={onClose} />
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      </>
    );
  }

  if (!detail.data) {
    return (
      <>
        <PageHeader title={title} onBack={onClose} />
        <LoadingState />
      </>
    );
  }

  return (
    <PolicyFormBody
      clusterId={clusterId}
      clusterName={clusterName}
      namespace={namespace}
      resource={resource}
      target={target}
      detail={detail.data}
      onClose={onClose}
    />
  );
}

function PolicyFormBody({
  clusterId,
  clusterName,
  namespace,
  resource,
  target,
  detail,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesPolicyResource;
  target: KubernetesPolicyResourceSummary | null;
  detail: KubernetesPolicyResourceDetail | null;
  onClose: () => void;
}) {
  const editing = target !== null;
  const kind = policyKindLabel(resource);
  const create = useCreatePolicyResource();
  const update = useUpdatePolicyResource();
  const pending = create.isPending || update.isPending;
  const error = create.error ?? update.error;
  const [previewed, setPreviewed] = useState<PolicyCreateSpec | null>(null);
  const previewKey = useSubmissionKey(previewed === null);
  const applyKey = useSubmissionKey(previewed !== null);
  const [name, setName] = useState(target?.name ?? "");

  /*
   * The name is the first section, so a fault in it outranks anything the kind's
   * own sections report. Handed to every editor so exactly one message is on
   * screen: an editor whose section is not the one being reported stays quiet,
   * the same way it would if its own draft were clean.
   */
  const earlier = editing ? null : policyNameProblem(name);
  const quota = useQuotaEditor(detail, earlier);
  const limitRange = useLimitRangeEditor(detail, earlier);
  const networkPolicy = useNetworkPolicyEditor(detail, earlier);
  const budget = useDisruptionBudgetEditor(detail, editing, earlier);
  const priorityClass = usePriorityClassEditor(detail, editing, earlier);
  const editor =
    resource === "resourcequotas"
      ? quota
      : resource === "limitranges"
        ? limitRange
        : resource === "networkpolicies"
          ? networkPolicy
          : resource === "poddisruptionbudgets"
            ? budget
            : priorityClass;

  /*
   * The first problem in the form, read top to bottom: the name, then whatever
   * the chosen kind's own sections report. One at a time and named where it can
   * be fixed, rather than a list at the bottom an operator has to map back onto
   * fields — the form is longer than the screen for every kind but PriorityClass.
   */
  const problem = earlier ?? editor.problem;
  const problemIn = (section: string) =>
    problem?.section === section ? problem.message : undefined;

  const submit = (dryRun: boolean, spec: PolicyCreateSpec) => {
    const shared = {
      clusterId,
      namespace,
      resource,
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    const request =
      editing && target
        ? update.mutateAsync({
            ...shared,
            name: target.name,
            uid: target.uid,
            resourceVersion: target.resource_version,
            spec: updateSpec(resource, spec, priorityClass.updateSpec()),
          })
        : create.mutateAsync({ ...shared, name: name.trim(), spec });
    void request
      .then(() => {
        if (dryRun) {
          setPreviewed(spec);
          return;
        }
        toast.success(
          `${kind} ${editing ? target?.name : name.trim()} 已${editing ? "更新" : "创建"}`,
        );
        onClose();
      })
      .catch(() => undefined);
  };

  return (
    <>
      <div className="grid gap-3">
        <PageHeader
          title={
            editing
              ? `编辑 ${kind} · ${target?.name}`
              : `创建 ${kind}${resource === "priorityclasses" ? "" : ` · ${namespace}`}`
          }
          onBack={onClose}
          backDisabled={pending}
        />

        {editing ? null : (
          <FormSection title={SECTIONS.basic} problem={problemIn(SECTIONS.basic)}>
            <div className="grid gap-1.5">
              <Label htmlFor="policy-name">名称</Label>
              <Input
                id="policy-name"
                value={name}
                autoComplete="off"
                spellCheck={false}
                placeholder="例如 team-a-quota"
                onChange={(event) => setName(event.target.value)}
              />
              <span className="text-subtle-foreground text-xs">
                合法的 DNS 子域名，最长 253 个字符；创建后不可修改
              </span>
            </div>
          </FormSection>
        )}
        {editor.fields}

        {/*
         * The message itself is up in the section that can fix it; down here,
         * next to the button it disables, what is missing is where to look.
         */}
        {problem ? <Alert tone="warning">「{problem.section}」中还有需要修正的项。</Alert> : null}
        {error ? <Alert tone="danger">{errorMessage(error)}</Alert> : null}

        <div className="flex flex-wrap items-center justify-end gap-3 pb-2">
          <span className="text-subtle-foreground text-xs">
            目标：{clusterName}
            {resource === "priorityclasses" ? "（集群级对象）" : ` / ${namespace}`}
          </span>
          <Button
            variant="primary"
            size="sm"
            disabled={problem !== null || pending}
            onClick={() => submit(true, editor.build())}
          >
            {pending ? "DryRun 预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={`确认${editing ? "更新" : "创建"} ${kind}`}
        description="DryRun 预检已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(resource === "priorityclasses" ? [] : [{ label: "命名空间", name: namespace }]),
          { label: kind, name: editing ? (target?.name ?? "") : name.trim(), id: target?.uid },
        ]}
        impacts={impacts(resource, editing)}
        confirmLabel={editing ? "确认更新" : "确认创建"}
        destructive={editing}
        pending={pending}
        error={error}
        onConfirm={() => previewed && submit(false, previewed)}
      />
    </>
  );
}

/**
 * The update request differs from the create request in exactly one place, so
 * the editors build the create shape and PriorityClass is mapped across here.
 */
function updateSpec(
  resource: KubernetesPolicyResource,
  spec: PolicyCreateSpec,
  priorityClass: { description?: string; global_default: boolean },
): PolicyUpdateSpec {
  if (resource === "priorityclasses") {
    return { priority_class: priorityClass };
  }
  return spec as PolicyUpdateSpec;
}

function impacts(resource: KubernetesPolicyResource, editing: boolean): string[] {
  const precondition =
    "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，更新会被拒绝而不是覆盖。";
  const replace = "整份托管 spec 会被本次提交替换，表单中未出现的字段将按当前值或缺省值提交。";
  if (resource === "resourcequotas") {
    return [
      "配额生效后，命名空间中超出额度的新建请求会被 Kubernetes 直接拒绝；已经运行的对象不会被回收。",
      "把额度调低到低于当前用量不会驱逐现有对象，但在用量降下来之前无法再创建同类资源。",
      ...(editing
        ? [replace, "scopes 在 Kubernetes 中创建后不可变，按当前值原样提交。", precondition]
        : []),
    ];
  }
  if (resource === "limitranges") {
    return [
      "LimitRange 只影响之后创建的对象：默认值会被注入未显式声明的容器，超出上下限的请求会被拒绝。",
      "已经运行的 Pod 不会被重新校验，也不会被驱逐。",
      ...(editing ? [replace, precondition] : []),
    ];
  }
  if (resource === "networkpolicies") {
    return [
      "一旦有 NetworkPolicy 选中某个 Pod，该 Pod 在对应方向上就变为默认拒绝，只放行规则允许的流量。",
      "策略是否真正生效取决于集群安装的 CNI；不支持 NetworkPolicy 的网络插件会忽略该对象。",
      ...(editing ? [replace, precondition] : []),
    ];
  }
  if (resource === "poddisruptionbudgets") {
    return [
      "预算只约束自愿中断（如节点排空、驱逐 API），不阻止节点故障等非自愿中断。",
      "预算过紧会让 kubectl drain 与节点维护长时间阻塞。",
      ...(editing ? ["selector 不在更新范围内，按当前值保留。", precondition] : []),
    ];
  }
  return [
    "PriorityClass 影响调度顺序：高优先级 Pod 在资源不足时可能抢占低优先级 Pod（除非 preemptionPolicy 为 Never）。",
    "设为集群默认后，未显式声明优先级的新 Pod 都会使用该 PriorityClass。",
    ...(editing ? ["value 在 Kubernetes 中不可变，只提交描述与集群默认开关。", precondition] : []),
  ];
}

type SpecEditor = {
  fields: ReactNode;
  /** What stops this draft from being submitted, and which section says so. */
  problem: PolicyProblem | null;
  build: () => PolicyCreateSpec;
};

function useQuotaEditor(
  detail: KubernetesPolicyResourceDetail | null,
  earlier: PolicyProblem | null,
): SpecEditor {
  const initial = detail?.resource_quota;
  const [pairs, setPairs] = useState<PairDraft[]>(
    initial ? mapToPairs(initial.hard) : [{ key: "requests.cpu", value: "" }],
  );
  const [scopes, setScopes] = useState<QuotaScope[]>((initial?.scopes ?? []) as QuotaScope[]);
  // Scopes are immutable in Kubernetes and the scope selector is not modelled by
  // this form; both are carried back unchanged so a quota edit cannot quietly
  // widen what the quota applies to.
  const scopeSelector = detail?.resource_quota?.scope_selector ?? [];
  const editing = detail !== null;

  const entries = pairs.filter((pair) => pair.key.trim() !== "");
  const problem =
    quantityPairsProblem(SECTIONS.quotaHard, pairs, "额度") ??
    (entries.length === 0
      ? at(SECTIONS.quotaHard, "请至少填写一项额度：没有 hard 的配额不限制任何东西。")
      : null);
  // The winning problem for this form: the name outranks these sections, so each
  // shows a message only when it is the one being reported — exactly one at a time.
  const shown = earlier ?? problem;

  const build = (): PolicyCreateSpec => ({
    resource_quota: {
      hard: pairsToMap(entries),
      ...(scopes.length > 0 ? { scopes } : {}),
      ...(scopeSelector.length > 0 ? { scope_selector: scopeSelector } : {}),
    },
  });

  const fields = (
    <>
      <FormSection
        title={SECTIONS.quotaHard}
        hint="资源名到 Kubernetes quantity，例如 requests.cpu = 10"
        problem={shown?.section === SECTIONS.quotaHard ? shown.message : undefined}
      >
        <PairList
          rows={pairs}
          onChange={setPairs}
          keyLabel="资源"
          valueLabel="额度"
          addLabel="添加额度"
          suggestions={QUOTA_RESOURCE_SUGGESTIONS}
        />
      </FormSection>
      <FormSection
        title={SECTIONS.quotaScopes}
        hint={editing ? "创建后不可变，按当前值提交" : "留空表示对命名空间中所有对象计量"}
      >
        <div className="flex flex-wrap gap-x-4 gap-y-1.5">
          {QUOTA_SCOPES.map((scope) => (
            <label key={scope} className="flex items-center gap-2 text-[13px]">
              <Checkbox
                checked={scopes.includes(scope)}
                disabled={editing}
                onCheckedChange={(checked) =>
                  setScopes(
                    checked === true
                      ? [...scopes, scope]
                      : scopes.filter((entry) => entry !== scope),
                  )
                }
              />
              {scope}
            </label>
          ))}
        </div>
        {scopeSelector.length > 0 ? (
          <Alert tone="info" className="mt-2">
            该对象带有 {scopeSelector.length} 条 scopeSelector 表达式，本表单不编辑它们，将按当前值
            原样提交；需要修改请使用 YAML。
          </Alert>
        ) : null}
      </FormSection>
    </>
  );

  return { fields, problem, build };
}

/** The five quantity groups of a LimitRange item, in the order they are shown. */
const LIMIT_GROUPS = [
  { key: "default", label: "默认限制" },
  { key: "defaultRequest", label: "默认请求" },
  { key: "max", label: "上限" },
  { key: "min", label: "下限" },
  { key: "ratio", label: "比值上限" },
] as const;

function limitItemsProblem(items: LimitItemDraft[]): PolicyProblem | null {
  const seen = new Map<LimitRangeType, number>();
  for (const [index, item] of items.entries()) {
    const section = limitItemSection(index);
    const first = seen.get(item.type);
    if (first !== undefined) {
      return at(
        section,
        `类型「${item.type}」已经在限制项 ${first + 1} 中出现；同一个 LimitRange 内每种类型只能有一项。`,
      );
    }
    seen.set(item.type, index);
    for (const group of LIMIT_GROUPS) {
      const fault = quantityPairsProblem(section, item[group.key], group.label);
      if (fault) {
        return fault;
      }
    }
    const filled = LIMIT_GROUPS.flatMap((group) => item[group.key]).filter(
      (pair) => pair.key.trim() !== "",
    );
    if (filled.length === 0) {
      return at(section, "请至少填写一组约束：一个空的限制项不会约束任何东西。");
    }
  }
  return null;
}

function useLimitRangeEditor(
  detail: KubernetesPolicyResourceDetail | null,
  earlier: PolicyProblem | null,
): SpecEditor {
  const initial = detail?.limit_range_detail?.items;
  const [items, setItems] = useState<LimitItemDraft[]>(
    initial && initial.length > 0 ? initial.map(limitItemDraft) : [emptyLimitItem("Container")],
  );

  const problem = limitItemsProblem(items);
  // The winning problem for this form: the name outranks these sections, so each
  // shows a message only when it is the one being reported — exactly one at a time.
  const shown = earlier ?? problem;

  const build = (): PolicyCreateSpec => ({
    limit_range: {
      items: items.map((item) => ({
        type: item.type,
        ...pairGroup("max", item.max),
        ...pairGroup("min", item.min),
        ...pairGroup("default", item.default),
        ...pairGroup("default_request", item.defaultRequest),
        ...pairGroup("max_limit_request_ratio", item.ratio),
      })),
    },
  });

  const update = (index: number, patch: Partial<LimitItemDraft>) =>
    setItems(items.map((item, position) => (position === index ? { ...item, ...patch } : item)));

  const fields = (
    <>
      {items.map((item, index) => (
        <FormSection
          key={index}
          title={limitItemSection(index)}
          hint="至少填写一组约束；同一类型只能出现一次"
          problem={shown?.section === limitItemSection(index) ? shown.message : undefined}
          action={
            items.length > 1 ? (
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`移除限制项 ${index + 1}`}
                onClick={() => setItems(items.filter((_, position) => position !== index))}
              >
                <X />
              </Button>
            ) : null
          }
        >
          <div className="grid gap-3">
            <Field label="类型" htmlFor={`limit-type-${index}`}>
              <Select
                value={item.type}
                onValueChange={(value) => update(index, { type: value as LimitRangeType })}
              >
                <SelectTrigger id={`limit-type-${index}`} className="w-64">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LIMIT_RANGE_TYPES.map((type) => (
                    <SelectItem key={type} value={type}>
                      {type}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <PairGroup
              label="默认限制（default）"
              rows={item.default}
              onChange={(rows) => update(index, { default: rows })}
            />
            <PairGroup
              label="默认请求（defaultRequest）"
              rows={item.defaultRequest}
              onChange={(rows) => update(index, { defaultRequest: rows })}
            />
            <PairGroup
              label="上限（max）"
              rows={item.max}
              onChange={(rows) => update(index, { max: rows })}
            />
            <PairGroup
              label="下限（min）"
              rows={item.min}
              onChange={(rows) => update(index, { min: rows })}
            />
            <PairGroup
              label="限制与请求比值上限（maxLimitRequestRatio）"
              rows={item.ratio}
              onChange={(rows) => update(index, { ratio: rows })}
            />
          </div>
        </FormSection>
      ))}
      <div>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => setItems([...items, emptyLimitItem(nextLimitType(items))])}
        >
          <Plus />
          添加限制项
        </Button>
      </div>
    </>
  );

  return { fields, problem, build };
}

/*
 * What a direction's rules still need.
 *
 * A peer with neither a CIDR nor a label selects nothing, and a port row with no
 * port is not a port — Kubernetes would refuse both, and the refusal names the
 * index rather than the row on screen.
 */
function networkRulesProblem(
  section: string,
  rules: RuleDraft[],
  direction: string,
): PolicyProblem | null {
  for (const [index, rule] of rules.entries()) {
    for (const [peerIndex, peer] of rule.peers.entries()) {
      const where = `规则 ${index + 1} 的${direction} ${peerIndex + 1}`;
      if (peer.mode === "ip") {
        const cidr = peer.cidr.trim();
        if (cidr === "") {
          return at(section, `${where}缺少 CIDR。`);
        }
        if (!cidr.includes("/")) {
          return at(section, `${where}的 CIDR 需要带前缀长度，例如 10.0.0.0/8。`);
        }
        continue;
      }
      const hasLabel = [...peer.podLabels, ...peer.namespaceLabels].some(
        (pair) => pair.key.trim() !== "",
      );
      if (!hasLabel) {
        return at(section, `${where}需要至少一个 Pod 标签或命名空间标签，否则它不选中任何对象。`);
      }
    }
    for (const [portIndex, port] of rule.ports.entries()) {
      if (port.port.trim() === "") {
        return at(section, `规则 ${index + 1} 的端口 ${portIndex + 1} 未填写端口号或名称。`);
      }
    }
  }
  return null;
}

function useNetworkPolicyEditor(
  detail: KubernetesPolicyResourceDetail | null,
  earlier: PolicyProblem | null,
): SpecEditor {
  const summary = detail?.network_policy;
  const initial = detail?.network_policy_detail;
  const [podLabels, setPodLabels] = useState<PairDraft[]>(
    mapToPairs(summary?.pod_selector?.match_labels ?? {}),
  );
  const [types, setTypes] = useState<("Ingress" | "Egress")[]>(
    (summary?.policy_types as ("Ingress" | "Egress")[] | undefined) ?? ["Ingress"],
  );
  const [ingress, setIngress] = useState<RuleDraft[]>((initial?.ingress ?? []).map(ruleDraft));
  const [egress, setEgress] = useState<RuleDraft[]>((initial?.egress ?? []).map(ruleDraft));
  // matchExpressions are not modelled by this form; keeping them means an edit
  // cannot silently widen a selector that was written in YAML.
  const podExpressions = summary?.pod_selector?.match_expressions ?? [];

  /*
   * Rules written for a direction that is no longer declared are reported in
   * 策略方向 rather than in their own section: unchecking a direction hides its
   * rule editor, so a message pointing there would point at nothing. Kubernetes
   * accepts such an object and then ignores those rules, which is the failure
   * that looks like success.
   */
  const strandedRules =
    !types.includes("Ingress") && ingress.length > 0
      ? at(
          SECTIONS.networkDirections,
          `已经写了 ${ingress.length} 条入站规则，但没有勾选 Ingress 方向，Kubernetes 会忽略它们。请勾选 Ingress，或勾选后删除这些规则。`,
        )
      : !types.includes("Egress") && egress.length > 0
        ? at(
            SECTIONS.networkDirections,
            `已经写了 ${egress.length} 条出站规则，但没有勾选 Egress 方向，Kubernetes 会忽略它们。请勾选 Egress，或勾选后删除这些规则。`,
          )
        : null;
  const problem =
    (types.length === 0
      ? at(SECTIONS.networkDirections, "请至少勾选一个方向：没有 policyTypes 的策略不会生效。")
      : null) ??
    strandedRules ??
    networkRulesProblem(SECTIONS.networkIngress, ingress, "来源") ??
    networkRulesProblem(SECTIONS.networkEgress, egress, "目标");
  // The winning problem for this form: the name outranks these sections, so each
  // shows a message only when it is the one being reported — exactly one at a time.
  const shown = earlier ?? problem;

  const build = (): PolicyCreateSpec => ({
    network_policy: {
      // The Server's selector shape always carries both halves, so the
      // expressions this form does not edit travel back as they were read.
      pod_selector: {
        match_labels: pairsToMap(podLabels.filter((pair) => pair.key.trim() !== "")),
        match_expressions: podExpressions,
      },
      policy_types: types,
      ...(ingress.length > 0 ? { ingress: ingress.map(buildRule) } : {}),
      ...(egress.length > 0 ? { egress: egress.map(buildRule) } : {}),
    },
  });

  const fields = (
    <>
      <FormSection title={SECTIONS.networkTarget} hint="留空表示命名空间中的所有 Pod">
        <PairList
          rows={podLabels}
          onChange={setPodLabels}
          keyLabel="标签键"
          valueLabel="标签值"
          addLabel="添加标签"
        />
        {podExpressions.length > 0 ? (
          <Alert tone="info" className="mt-2">
            该策略的 podSelector 还带有 {podExpressions.length} 条 matchExpressions，本表单不编辑
            它们，将按当前值原样提交。
          </Alert>
        ) : null}
      </FormSection>

      <FormSection
        title={SECTIONS.networkDirections}
        hint="被选中的 Pod 在勾选方向上默认拒绝，只放行下面的规则"
        problem={shown?.section === SECTIONS.networkDirections ? shown.message : undefined}
      >
        <div className="flex flex-wrap gap-x-4 gap-y-1.5">
          {(["Ingress", "Egress"] as const).map((type) => (
            <label key={type} className="flex items-center gap-2 text-[13px]">
              <Checkbox
                checked={types.includes(type)}
                onCheckedChange={(checked) =>
                  setTypes(
                    checked === true ? [...types, type] : types.filter((entry) => entry !== type),
                  )
                }
              />
              {type}
            </label>
          ))}
        </div>
      </FormSection>

      {types.includes("Ingress") ? (
        <RuleListEditor
          title={SECTIONS.networkIngress}
          rules={ingress}
          onChange={setIngress}
          direction="来源"
          problem={shown?.section === SECTIONS.networkIngress ? shown.message : undefined}
        />
      ) : null}
      {types.includes("Egress") ? (
        <RuleListEditor
          title={SECTIONS.networkEgress}
          rules={egress}
          onChange={setEgress}
          direction="目标"
          problem={shown?.section === SECTIONS.networkEgress ? shown.message : undefined}
        />
      ) : null}
    </>
  );

  return { fields, problem, build };
}

function disruptionBudgetProblem(draft: {
  editing: boolean;
  entries: PairDraft[];
  expressions: number;
  mode: "min_available" | "max_unavailable";
  value: string;
}): PolicyProblem | null {
  // The selector is not in the update range, so an edit is not asked to justify
  // one it cannot change.
  if (!draft.editing && draft.entries.length === 0 && draft.expressions === 0) {
    return at(SECTIONS.budgetSelector, "请至少填写一个标签：预算需要明确它保护哪些 Pod。");
  }
  const value = draft.value.trim();
  if (value === "") {
    return at(SECTIONS.budget, "请填写数量。");
  }
  if (!BUDGET_VALUE.test(value)) {
    return at(SECTIONS.budget, "数量必须是 Pod 个数或百分比，例如 2 或 50%。");
  }
  if (value.endsWith("%") && Number(value.slice(0, -1)) > 100) {
    return at(SECTIONS.budget, "百分比不能超过 100%。");
  }
  return null;
}

function useDisruptionBudgetEditor(
  detail: KubernetesPolicyResourceDetail | null,
  editing: boolean,
  earlier: PolicyProblem | null,
): SpecEditor {
  const summary = detail?.disruption_budget;
  const [selectorLabels, setSelectorLabels] = useState<PairDraft[]>(
    summary?.selector?.match_labels
      ? mapToPairs(summary.selector.match_labels)
      : [{ key: "app", value: "" }],
  );
  const [mode, setMode] = useState<"min_available" | "max_unavailable">(
    summary?.max_unavailable ? "max_unavailable" : "min_available",
  );
  const [value, setValue] = useState(summary?.min_available || summary?.max_unavailable || "1");
  const [eviction, setEviction] = useState(
    detail?.disruption_budget_detail?.unhealthy_pod_eviction_policy || DEFAULT_OPTION,
  );
  const selectorExpressions = summary?.selector?.match_expressions ?? [];

  const entries = selectorLabels.filter((pair) => pair.key.trim() !== "");
  const problem = disruptionBudgetProblem({
    editing,
    entries,
    expressions: selectorExpressions.length,
    mode,
    value,
  });
  // The winning problem for this form: the name outranks these sections, so each
  // shows a message only when it is the one being reported — exactly one at a time.
  const shown = earlier ?? problem;

  const build = (): PolicyCreateSpec => ({
    disruption_budget: {
      selector: {
        match_labels: pairsToMap(entries),
        match_expressions: selectorExpressions,
      },
      [mode]: value.trim(),
      ...(eviction === DEFAULT_OPTION
        ? {}
        : { unhealthy_pod_eviction_policy: eviction as "IfHealthyBudget" | "AlwaysAllow" }),
    },
  });

  const fields = (
    <>
      <FormSection
        title={SECTIONS.budgetSelector}
        hint={editing ? "selector 不在更新范围内，按当前值保留" : "预算作用于匹配这些标签的 Pod"}
        problem={shown?.section === SECTIONS.budgetSelector ? shown.message : undefined}
      >
        <PairList
          rows={selectorLabels}
          onChange={setSelectorLabels}
          keyLabel="标签键"
          valueLabel="标签值"
          addLabel="添加标签"
          disabled={editing}
        />
      </FormSection>
      <FormSection
        title={SECTIONS.budget}
        hint="可写 Pod 个数或百分比，例如 2 或 50%"
        problem={shown?.section === SECTIONS.budget ? shown.message : undefined}
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="约束方式" htmlFor="pdb-mode">
            <Select
              value={mode}
              onValueChange={(next) => setMode(next as "min_available" | "max_unavailable")}
            >
              <SelectTrigger id="pdb-mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="min_available">minAvailable（至少保持可用）</SelectItem>
                <SelectItem value="max_unavailable">maxUnavailable（最多允许不可用）</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label="数量" htmlFor="pdb-value">
            <Input
              id="pdb-value"
              value={value}
              autoComplete="off"
              placeholder="2 或 50%"
              onChange={(event) => setValue(event.target.value)}
            />
          </Field>
          <Field
            label="不健康 Pod 驱逐策略"
            htmlFor="pdb-eviction"
            hint="AlwaysAllow 允许驱逐尚未就绪的 Pod"
          >
            <Select value={eviction} onValueChange={setEviction}>
              <SelectTrigger id="pdb-eviction">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_OPTION}>集群默认（IfHealthyBudget）</SelectItem>
                <SelectItem value="IfHealthyBudget">IfHealthyBudget</SelectItem>
                <SelectItem value="AlwaysAllow">AlwaysAllow</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </div>
      </FormSection>
    </>
  );

  return { fields, problem, build };
}

type PriorityClassEditor = SpecEditor & {
  updateSpec: () => { description?: string; global_default: boolean };
};

function usePriorityClassEditor(
  detail: KubernetesPolicyResourceDetail | null,
  editing: boolean,
  earlier: PolicyProblem | null,
): PriorityClassEditor {
  const summary = detail?.priority_class;
  const [value, setValue] = useState(summary ? String(summary.value) : "");
  const [globalDefault, setGlobalDefault] = useState(summary?.global_default ?? false);
  const [preemption, setPreemption] = useState(summary?.preemption_policy || DEFAULT_OPTION);
  const [description, setDescription] = useState(summary?.description ?? "");

  const parsed = Number(value.trim());
  /*
   * Only the value is checked, and only while creating: an edit submits the
   * description and the default switch, and Kubernetes freezes the value at
   * creation. The input accepts digits only, so a negative priority — which
   * Kubernetes does allow — cannot be written here at all.
   */
  const problem = editing
    ? null
    : value.trim() === ""
      ? at(SECTIONS.priority, "请填写优先级值。")
      : !Number.isInteger(parsed)
        ? at(SECTIONS.priority, "优先级值必须是整数。")
        : parsed > 1_000_000_000
          ? at(SECTIONS.priority, "优先级值不能超过 1000000000。")
          : null;
  // The winning problem for this form: the name outranks these sections, so each
  // shows a message only when it is the one being reported — exactly one at a time.
  const shown = earlier ?? problem;

  const build = (): PolicyCreateSpec => ({
    priority_class: {
      value: parsed,
      global_default: globalDefault,
      ...(preemption === DEFAULT_OPTION
        ? {}
        : { preemption_policy: preemption as "PreemptLowerPriority" | "Never" }),
      ...(description.trim() ? { description: description.trim() } : {}),
    },
  });

  const updateSpec = () => ({
    global_default: globalDefault,
    ...(description.trim() ? { description: description.trim() } : {}),
  });

  const fields = (
    <FormSection
      title={SECTIONS.priority}
      problem={shown?.section === SECTIONS.priority ? shown.message : undefined}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          label="优先级值"
          htmlFor="priority-value"
          hint={
            editing
              ? "Kubernetes 不允许修改已创建 PriorityClass 的 value"
              : "整数，越大越优先，上限 1000000000"
          }
        >
          <NumericInput
            id="priority-value"
            value={value}
            placeholder="100000"
            disabled={editing}
            onValueChange={setValue}
          />
        </Field>
        <Field
          label="抢占策略"
          htmlFor="priority-preemption"
          hint={editing ? "创建后不再由类型化接口修改" : "Never 表示只排队不抢占低优先级 Pod"}
        >
          <Select value={preemption} onValueChange={setPreemption} disabled={editing}>
            <SelectTrigger id="priority-preemption">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT_OPTION}>默认（PreemptLowerPriority）</SelectItem>
              <SelectItem value="PreemptLowerPriority">PreemptLowerPriority</SelectItem>
              <SelectItem value="Never">Never</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>
      <div className="mt-3 grid gap-1.5">
        <Label htmlFor="priority-description">描述</Label>
        <Input
          id="priority-description"
          value={description}
          autoComplete="off"
          placeholder="这个优先级用于什么"
          onChange={(event) => setDescription(event.target.value)}
        />
      </div>
      <label className="mt-3 flex items-center gap-2 text-[13px]">
        <Checkbox
          checked={globalDefault}
          onCheckedChange={(checked) => setGlobalDefault(checked === true)}
        />
        设为集群默认优先级
      </label>
      <span className="text-subtle-foreground mt-1 block text-xs">
        一个集群最多只能有一个默认 PriorityClass；已有默认值时 Kubernetes 会拒绝本次写入。
      </span>
    </FormSection>
  );

  return { fields, problem, build, updateSpec };
}

type PeerDraft = {
  mode: "selector" | "ip";
  podLabels: PairDraft[];
  namespaceLabels: PairDraft[];
  cidr: string;
  except: string;
};

type PortDraft = { protocol: string; port: string; endPort: string };

type RuleDraft = { peers: PeerDraft[]; ports: PortDraft[] };

type LimitItemDraft = {
  type: LimitRangeType;
  max: PairDraft[];
  min: PairDraft[];
  default: PairDraft[];
  defaultRequest: PairDraft[];
  ratio: PairDraft[];
};

function RuleListEditor({
  title,
  rules,
  onChange,
  direction,
  problem,
}: {
  title: string;
  rules: RuleDraft[];
  onChange: (rules: RuleDraft[]) => void;
  /** What the peers of this direction are called: 来源 or 目标. */
  direction: string;
  problem?: string;
}) {
  const update = (index: number, patch: Partial<RuleDraft>) =>
    onChange(rules.map((rule, position) => (position === index ? { ...rule, ...patch } : rule)));

  return (
    <FormSection
      title={title}
      hint={rules.length === 0 ? "没有规则表示该方向全部拒绝" : undefined}
      problem={problem}
      action={
        <Button
          size="sm"
          variant="secondary"
          onClick={() => onChange([...rules, { peers: [], ports: [] }])}
        >
          <Plus />
          添加规则
        </Button>
      }
    >
      <div className="grid gap-3">
        {rules.map((rule, index) => (
          <div key={index} className="border-border rounded-panel border p-3">
            <div className="mb-2 flex items-center justify-between">
              <span className="text-foreground text-[13px] font-medium">规则 {index + 1}</span>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`移除${title} ${index + 1}`}
                onClick={() => onChange(rules.filter((_, position) => position !== index))}
              >
                <X />
              </Button>
            </div>

            <span className="text-subtle-foreground text-xs">
              {rule.peers.length === 0
                ? `没有${direction}表示放行任意${direction}`
                : `${rule.peers.length} 个${direction}`}
            </span>
            <div className="mt-2 grid gap-2">
              {rule.peers.map((peer, peerIndex) => (
                <PeerEditor
                  key={peerIndex}
                  peer={peer}
                  label={`${direction} ${peerIndex + 1}`}
                  onChange={(next) =>
                    update(index, {
                      peers: rule.peers.map((entry, position) =>
                        position === peerIndex ? next : entry,
                      ),
                    })
                  }
                  onRemove={() =>
                    update(index, {
                      peers: rule.peers.filter((_, position) => position !== peerIndex),
                    })
                  }
                />
              ))}
              <div>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() =>
                    update(index, {
                      peers: [
                        ...rule.peers,
                        {
                          mode: "selector",
                          podLabels: [{ key: "", value: "" }],
                          namespaceLabels: [],
                          cidr: "",
                          except: "",
                        },
                      ],
                    })
                  }
                >
                  <Plus />
                  添加{direction}
                </Button>
              </div>
            </div>

            <div className="mt-3 grid gap-2">
              <span className="text-subtle-foreground text-xs">
                {rule.ports.length === 0 ? "没有端口表示放行所有端口" : "端口"}
              </span>
              {rule.ports.map((port, portIndex) => (
                <div
                  key={portIndex}
                  className="grid grid-cols-[7rem_1fr_1fr_auto] items-center gap-2"
                >
                  <Select
                    value={port.protocol || DEFAULT_OPTION}
                    onValueChange={(value) =>
                      update(index, {
                        ports: rule.ports.map((entry, position) =>
                          position === portIndex
                            ? { ...entry, protocol: value === DEFAULT_OPTION ? "" : value }
                            : entry,
                        ),
                      })
                    }
                  >
                    <SelectTrigger aria-label={`端口 ${portIndex + 1} 协议`}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={DEFAULT_OPTION}>TCP（默认）</SelectItem>
                      {NETWORK_PROTOCOLS.map((protocol) => (
                        <SelectItem key={protocol} value={protocol}>
                          {protocol}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Input
                    value={port.port}
                    aria-label={`端口 ${portIndex + 1}`}
                    placeholder="端口号或名称"
                    autoComplete="off"
                    onChange={(event) =>
                      update(index, {
                        ports: rule.ports.map((entry, position) =>
                          position === portIndex ? { ...entry, port: event.target.value } : entry,
                        ),
                      })
                    }
                  />
                  <Input
                    value={port.endPort}
                    aria-label={`端口 ${portIndex + 1} 范围结束`}
                    placeholder="范围结束（可选）"
                    autoComplete="off"
                    inputMode="numeric"
                    onChange={(event) =>
                      update(index, {
                        ports: rule.ports.map((entry, position) =>
                          position === portIndex
                            ? { ...entry, endPort: event.target.value }
                            : entry,
                        ),
                      })
                    }
                  />
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={`移除端口 ${portIndex + 1}`}
                    onClick={() =>
                      update(index, {
                        ports: rule.ports.filter((_, position) => position !== portIndex),
                      })
                    }
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
                    update(index, {
                      ports: [...rule.ports, { protocol: "", port: "", endPort: "" }],
                    })
                  }
                >
                  <Plus />
                  添加端口
                </Button>
              </div>
            </div>
          </div>
        ))}
      </div>
    </FormSection>
  );
}

function PeerEditor({
  peer,
  label,
  onChange,
  onRemove,
}: {
  peer: PeerDraft;
  label: string;
  onChange: (peer: PeerDraft) => void;
  onRemove: () => void;
}) {
  return (
    <div className="bg-surface-muted rounded-control grid gap-2 p-2">
      <div className="flex items-center gap-2">
        <Select
          value={peer.mode}
          onValueChange={(value) => onChange({ ...peer, mode: value as "selector" | "ip" })}
        >
          <SelectTrigger className="w-56" aria-label={`${label} 类型`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="selector">按标签选择 Pod / 命名空间</SelectItem>
            <SelectItem value="ip">按 IP 段</SelectItem>
          </SelectContent>
        </Select>
        <span className="text-subtle-foreground text-xs">{label}</span>
        <Button size="icon-sm" variant="ghost" aria-label={`移除${label}`} onClick={onRemove}>
          <X />
        </Button>
      </div>
      {peer.mode === "ip" ? (
        <div className="grid gap-2 sm:grid-cols-2">
          <Input
            value={peer.cidr}
            aria-label={`${label} CIDR`}
            placeholder="10.0.0.0/8"
            autoComplete="off"
            onChange={(event) => onChange({ ...peer, cidr: event.target.value })}
          />
          <Input
            value={peer.except}
            aria-label={`${label} 排除段`}
            placeholder="排除段，逗号分隔（可选）"
            autoComplete="off"
            onChange={(event) => onChange({ ...peer, except: event.target.value })}
          />
        </div>
      ) : (
        <div className="grid gap-2">
          <PairList
            rows={peer.podLabels}
            onChange={(rows) => onChange({ ...peer, podLabels: rows })}
            keyLabel="Pod 标签键"
            valueLabel="Pod 标签值"
            addLabel="添加 Pod 标签"
          />
          <PairList
            rows={peer.namespaceLabels}
            onChange={(rows) => onChange({ ...peer, namespaceLabels: rows })}
            keyLabel="命名空间标签键"
            valueLabel="命名空间标签值"
            addLabel="添加命名空间标签"
          />
        </div>
      )}
    </div>
  );
}

function buildRule(rule: RuleDraft): KubernetesNetworkPolicyRule {
  const peers: KubernetesNetworkPolicyPeer[] = rule.peers.map((peer) => {
    if (peer.mode === "ip") {
      const except = peer.except
        .split(",")
        .map((entry) => entry.trim())
        .filter(Boolean);
      return { ip_block: { cidr: peer.cidr.trim(), ...(except.length > 0 ? { except } : {}) } };
    }
    const pod = peer.podLabels.filter((pair) => pair.key.trim() !== "");
    const namespace = peer.namespaceLabels.filter((pair) => pair.key.trim() !== "");
    return {
      ...(pod.length > 0
        ? { pod_selector: { match_labels: pairsToMap(pod), match_expressions: [] } }
        : {}),
      ...(namespace.length > 0
        ? { namespace_selector: { match_labels: pairsToMap(namespace), match_expressions: [] } }
        : {}),
    };
  });
  const ports: KubernetesNetworkPolicyPort[] = rule.ports.map((port) => ({
    ...(port.protocol ? { protocol: port.protocol as "TCP" | "UDP" | "SCTP" } : {}),
    port: port.port.trim(),
    ...(port.endPort.trim() ? { end_port: Number(port.endPort.trim()) } : {}),
  }));
  return {
    ...(peers.length > 0 ? { peers } : {}),
    ...(ports.length > 0 ? { ports } : {}),
  };
}

function ruleDraft(rule: KubernetesNetworkPolicyRule): RuleDraft {
  return {
    peers: (rule.peers ?? []).map((peer) => ({
      mode: peer.ip_block ? ("ip" as const) : ("selector" as const),
      podLabels: mapToPairs(peer.pod_selector?.match_labels ?? {}),
      namespaceLabels: mapToPairs(peer.namespace_selector?.match_labels ?? {}),
      cidr: peer.ip_block?.cidr ?? "",
      except: (peer.ip_block?.except ?? []).join(", "),
    })),
    ports: (rule.ports ?? []).map((port) => ({
      protocol: port.protocol ?? "",
      port: port.port ?? "",
      endPort: port.end_port ? String(port.end_port) : "",
    })),
  };
}

function limitItemDraft(item: KubernetesLimitRangeItem): LimitItemDraft {
  return {
    type: item.type as LimitRangeType,
    max: mapToPairs(item.max ?? {}),
    min: mapToPairs(item.min ?? {}),
    default: mapToPairs(item.default ?? {}),
    defaultRequest: mapToPairs(item.default_request ?? {}),
    ratio: mapToPairs(item.max_limit_request_ratio ?? {}),
  };
}

function emptyLimitItem(type: LimitRangeType): LimitItemDraft {
  return {
    type,
    max: [],
    min: [],
    default: type === "Container" ? [{ key: "cpu", value: "" }] : [],
    defaultRequest: [],
    ratio: [],
  };
}

function nextLimitType(items: LimitItemDraft[]): LimitRangeType {
  return LIMIT_RANGE_TYPES.find((type) => !items.some((item) => item.type === type)) ?? "Container";
}

function pairGroup(key: string, rows: PairDraft[]): Record<string, Record<string, string>> {
  const entries = rows.filter((pair) => pair.key.trim() !== "");
  return entries.length > 0 ? { [key]: pairsToMap(entries) } : {};
}

function mapToPairs(values: Record<string, string>): PairDraft[] {
  return Object.entries(values).map(([key, value]) => ({ key, value }));
}

function pairsToMap(rows: PairDraft[]): Record<string, string> {
  return Object.fromEntries(rows.map((row) => [row.key.trim(), row.value.trim()]));
}

function PairGroup({
  label,
  rows,
  onChange,
}: {
  label: string;
  rows: PairDraft[];
  onChange: (rows: PairDraft[]) => void;
}) {
  return (
    <div className="grid gap-1.5">
      <span className="text-foreground text-[13px]">{label}</span>
      <PairList
        rows={rows}
        onChange={onChange}
        keyLabel="资源"
        valueLabel="数量"
        addLabel={`添加${label.split("（")[0]}`}
      />
    </div>
  );
}

function FormSection({
  title,
  hint,
  action,
  problem,
  children,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
  /** The current blocking problem, when it is this section that carries it. */
  problem?: string;
  children: ReactNode;
}) {
  return (
    <section>
      <div className="mb-2 flex items-baseline justify-between gap-2">
        <div className="flex flex-wrap items-baseline gap-2">
          <h4 className="text-foreground text-[13px] font-medium">{title}</h4>
          {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
        </div>
        {action}
      </div>
      {problem ? (
        <Alert tone="warning" className="mb-2">
          {problem}
        </Alert>
      ) : null}
      {children}
    </section>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
    </div>
  );
}

function PairList({
  rows,
  onChange,
  keyLabel,
  valueLabel,
  addLabel,
  suggestions,
  disabled = false,
}: {
  rows: PairDraft[];
  onChange: (rows: PairDraft[]) => void;
  keyLabel: string;
  valueLabel: string;
  addLabel: string;
  /** Offered through a datalist; the field stays free text. */
  suggestions?: readonly string[];
  disabled?: boolean;
}) {
  const listId = suggestions ? `${keyLabel}-suggestions` : undefined;
  const update = (index: number, patch: Partial<PairDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <div className="grid gap-2">
      {suggestions ? (
        <datalist id={listId}>
          {suggestions.map((suggestion) => (
            <option key={suggestion} value={suggestion} />
          ))}
        </datalist>
      ) : null}
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
          <Input
            value={row.key}
            list={listId}
            aria-label={`${keyLabel} ${index + 1}`}
            placeholder={keyLabel}
            autoComplete="off"
            spellCheck={false}
            disabled={disabled}
            onChange={(event) => update(index, { key: event.target.value })}
          />
          <Input
            value={row.value}
            aria-label={`${valueLabel} ${index + 1}`}
            placeholder={valueLabel}
            autoComplete="off"
            spellCheck={false}
            disabled={disabled}
            onChange={(event) => update(index, { value: event.target.value })}
          />
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`移除${keyLabel} ${index + 1}`}
            disabled={disabled}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          >
            <X />
          </Button>
        </div>
      ))}
      {disabled ? null : (
        <div>
          <Button
            size="sm"
            variant="secondary"
            onClick={() => onChange([...rows, { key: "", value: "" }])}
          >
            <Plus />
            {addLabel}
          </Button>
        </div>
      )}
    </div>
  );
}
