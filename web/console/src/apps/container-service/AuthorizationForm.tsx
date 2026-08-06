import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import {
  isNamespacedAuthorization,
  useAuthorizationResource,
  useCreateAuthorizationResource,
  useUpdateAuthorizationResource,
  type AuthorizationCreateSpec,
} from "@/api/queries/authorization";
import type {
  KubernetesAuthorizationResource,
  KubernetesAuthorizationResourceDetail,
} from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { authorizationKindLabel, hasRules, isBinding } from "./authorization-catalog";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;

/*
 * The name rule each family actually has.
 *
 * Kubernetes validates RBAC names as path segments, not as DNS subdomains —
 * which is why `system:basic-user` and `kubeadm:cluster-admins` exist. Only
 * ServiceAccounts are core/v1 objects and really do need a DNS subdomain.
 */
function nameProblem(resource: KubernetesAuthorizationResource, name: string): string | null {
  if (name === "") {
    return "请填写名称。";
  }
  if (name.length > 253) {
    return "名称最长 253 个字符。";
  }
  if (resource === "serviceaccounts") {
    return DNS_SUBDOMAIN.test(name)
      ? null
      : "名称必须是合法的 DNS 子域名：只能包含小写字母、数字、连字符和点，并以字母或数字开头和结尾。";
  }
  if (name === "." || name === ".." || name.includes("/") || name.includes("%")) {
    return "名称不能是 . 或 ..，也不能包含 / 和 %。";
  }
  return null;
}
const AUTOMOUNT_DEFAULT = "__default__";

type RuleDraft = {
  verbs: string;
  apiGroups: string;
  resources: string;
  resourceNames: string;
  nonResourceUrls: string;
};

type SubjectDraft = {
  kind: "ServiceAccount" | "User" | "Group";
  name: string;
  namespace: string;
};

const EMPTY_RULE: RuleDraft = {
  verbs: "get, list, watch",
  apiGroups: "",
  resources: "",
  resourceNames: "",
  nonResourceUrls: "",
};

type SectionKey = "basic" | "token" | "rules" | "role" | "subjects";

/** The titles the sections are rendered with, to point at one from elsewhere. */
const SECTION_LABELS: Record<SectionKey, string> = {
  basic: "基本信息",
  token: "Token",
  rules: "规则",
  role: "引用角色",
  subjects: "主体",
};

/** The one thing currently blocking submission, and where it can be fixed. */
type FormProblem = { section: SectionKey; message: string };

function at(section: SectionKey, message: string): FormProblem {
  return { section, message };
}

/*
 * The first problem in the form, read top to bottom.
 *
 * One at a time, and named where it can be fixed: a list of every fault at the
 * bottom of the page is a list an operator has to map back onto fields, and the
 * page is longer than the screen. Reported in the order the sections appear, so
 * fixing what is reported moves down the form rather than around it.
 */
function authorizationProblem(draft: {
  resource: KubernetesAuthorizationResource;
  editing: boolean;
  name: string;
  rules: RuleDraft[];
  subjects: SubjectDraft[];
  roleName: string;
}): FormProblem | null {
  const { resource, editing, name, rules, subjects, roleName } = draft;
  if (!editing) {
    const fault = nameProblem(resource, name.trim());
    if (fault) {
      return at("basic", fault);
    }
  }
  if (hasRules(resource)) {
    const fault = rulesProblem(resource, rules);
    if (fault) {
      return fault;
    }
  }
  if (isBinding(resource)) {
    // roleRef is immutable, so an edit is not asked to supply one it cannot send.
    if (!editing && roleName.trim() === "") {
      return at("role", "请填写角色名称。");
    }
    return subjectsProblem(subjects);
  }
  return null;
}

/*
 * A rule's shape, checked against the same constraints the API Server applies.
 *
 * Two of them are about where a rule lives rather than what it says: a
 * namespaced Role cannot grant non-resource URLs at all, and no rule may mix
 * them with regular resources. Kubernetes reports both, but only after a round
 * trip and without pointing at the row.
 */
function rulesProblem(
  resource: KubernetesAuthorizationResource,
  rules: RuleDraft[],
): FormProblem | null {
  if (rules.length === 0) {
    return at("rules", "请至少添加一条规则：一个没有规则的角色不授予任何权限。");
  }
  for (const [index, rule] of rules.entries()) {
    const where = `第 ${index + 1} 条规则`;
    if (splitList(rule.verbs).length === 0) {
      return at("rules", `${where}缺少动作（verbs），例如 get、list。`);
    }
    const resources = splitList(rule.resources);
    const urls = splitList(rule.nonResourceUrls);
    if (resources.length === 0 && urls.length === 0) {
      return at("rules", `${where}需要填写资源，或填写非资源 URL。`);
    }
    if (urls.length > 0 && resource === "roles") {
      return at(
        "rules",
        `${where}声明了非资源 URL，而 Role 只作用于命名空间内的资源；这类规则只能写在 ClusterRole 上。`,
      );
    }
    if (urls.length > 0 && resources.length > 0) {
      return at(
        "rules",
        `${where}同时声明了资源和非资源 URL，Kubernetes 不接受一条规则覆盖两者，请拆成两条。`,
      );
    }
    if (urls.length > 0 && splitList(rule.resourceNames).length > 0) {
      return at("rules", `${where}同时声明了非资源 URL 和资源名称，后者只对资源规则有意义。`);
    }
  }
  return null;
}

function subjectsProblem(subjects: SubjectDraft[]): FormProblem | null {
  if (subjects.length === 0) {
    return at("subjects", "请至少添加一个主体：一个没有主体的绑定不把权限授予任何人。");
  }
  for (const [index, subject] of subjects.entries()) {
    const where = `第 ${index + 1} 个主体`;
    if (subject.name.trim() === "") {
      return at("subjects", `${where}缺少名称。`);
    }
    if (subject.kind === "ServiceAccount" && subject.namespace.trim() === "") {
      return at("subjects", `${where}是 ServiceAccount，需要填写它所在的命名空间。`);
    }
  }
  return null;
}

/**
 * Creates or replaces one RBAC object.
 *
 * Editing loads the object first: an update replaces the whole rule or subject
 * list and carries the UID and resourceVersion it was read at.
 */
export function AuthorizationForm({
  clusterId,
  clusterName,
  namespace,
  resource,
  editingName,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesAuthorizationResource;
  /** Set when editing; null when creating. */
  editingName: string | null;
  onClose: () => void;
}) {
  const existing = useAuthorizationResource(clusterId, namespace, resource, editingName);
  const title = `编辑 ${authorizationKindLabel(resource)} · ${editingName}`;

  if (editingName && existing.isLoading) {
    return (
      <>
        <PageHeader title={title} onBack={onClose} />
        <LoadingState />
      </>
    );
  }
  if (editingName && (existing.error || !existing.data)) {
    return (
      <>
        <PageHeader title={title} onBack={onClose} />
        <ErrorState error={existing.error} onRetry={() => void existing.refetch()} />
      </>
    );
  }

  return (
    <AuthorizationEditor
      clusterId={clusterId}
      clusterName={clusterName}
      namespace={namespace}
      resource={resource}
      existing={editingName ? (existing.data as KubernetesAuthorizationResourceDetail) : null}
      onClose={onClose}
    />
  );
}

function AuthorizationEditor({
  clusterId,
  clusterName,
  namespace,
  resource,
  existing,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesAuthorizationResource;
  existing: KubernetesAuthorizationResourceDetail | null;
  onClose: () => void;
}) {
  const create = useCreateAuthorizationResource();
  const update = useUpdateAuthorizationResource();
  const mutation = existing ? update : create;
  const kind = authorizationKindLabel(resource);
  const [previewed, setPreviewed] = useState<AuthorizationCreateSpec | null>(null);
  const previewKey = useSubmissionKey(previewed === null);
  const applyKey = useSubmissionKey(previewed !== null);
  // Pinned at mount: a background refetch must not re-arm the precondition with
  // a version the form's contents were never based on.
  const [pinned] = useState(() =>
    existing ? { uid: existing.uid, resourceVersion: existing.resource_version } : null,
  );

  const [name, setName] = useState(existing?.name ?? "");
  const [automount, setAutomount] = useState(
    existing?.automount_service_account_token === undefined
      ? AUTOMOUNT_DEFAULT
      : String(existing.automount_service_account_token),
  );
  const [rules, setRules] = useState<RuleDraft[]>(
    existing && existing.rules.length > 0
      ? existing.rules.map((rule) => ({
          verbs: rule.verbs.join(", "),
          apiGroups: rule.api_groups.join(", "),
          resources: rule.resources.join(", "),
          resourceNames: rule.resource_names.join(", "),
          nonResourceUrls: rule.non_resource_urls.join(", "),
        }))
      : [{ ...EMPTY_RULE }],
  );
  const [subjects, setSubjects] = useState<SubjectDraft[]>(
    existing && existing.subjects.length > 0
      ? existing.subjects.map((subject) => ({
          kind: subject.kind,
          name: subject.name,
          namespace: subject.namespace,
        }))
      : [{ kind: "ServiceAccount", name: "", namespace }],
  );
  const [roleKind, setRoleKind] = useState<"Role" | "ClusterRole">(
    existing?.role_ref?.kind ?? (resource === "clusterrolebindings" ? "ClusterRole" : "Role"),
  );
  const [roleName, setRoleName] = useState(existing?.role_ref?.name ?? "");

  const withRules = hasRules(resource);
  const binding = isBinding(resource);

  const problem = authorizationProblem({
    resource,
    editing: existing !== null,
    name,
    rules,
    subjects,
    roleName,
  });
  const problemIn = (section: SectionKey) =>
    problem?.section === section ? problem.message : undefined;

  const buildSpec = (): AuthorizationCreateSpec => ({
    ...(resource === "serviceaccounts" && automount !== AUTOMOUNT_DEFAULT
      ? { automount_service_account_token: automount === "true" }
      : {}),
    ...(withRules
      ? {
          rules: rules.map((rule) => ({
            verbs: splitList(rule.verbs),
            // An empty API group string is the core group, and dropping empty
            // entries here would silently change which resources a rule covers.
            api_groups: splitList(rule.apiGroups, true),
            resources: splitList(rule.resources),
            resource_names: splitList(rule.resourceNames),
            non_resource_urls: splitList(rule.nonResourceUrls),
          })),
        }
      : {}),
    ...(binding
      ? {
          subjects: subjects.map((subject) => ({
            api_group:
              subject.kind === "ServiceAccount"
                ? ("" as const)
                : ("rbac.authorization.k8s.io" as const),
            kind: subject.kind,
            name: subject.name.trim(),
            namespace: subject.kind === "ServiceAccount" ? subject.namespace.trim() : "",
          })),
          // roleRef is immutable, so it is only ever sent on creation.
          ...(existing
            ? {}
            : {
                role_ref: {
                  api_group: "rbac.authorization.k8s.io" as const,
                  kind: roleKind,
                  name: roleName.trim(),
                },
              }),
        }
      : {}),
  });

  const submit = (dryRun: boolean, spec: AuthorizationCreateSpec) => {
    const shared = {
      clusterId,
      namespace,
      resource,
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    const request = existing
      ? update.mutateAsync({
          ...shared,
          spec,
          name: existing.name,
          uid: pinned?.uid ?? existing.uid,
          resourceVersion: pinned?.resourceVersion ?? existing.resource_version,
        })
      : create.mutateAsync({ ...shared, spec, name: name.trim() });
    void request
      .then(() => {
        if (dryRun) {
          setPreviewed(spec);
          return;
        }
        toast.success(`${kind} ${existing?.name ?? name.trim()} 已${existing ? "更新" : "创建"}`);
        onClose();
      })
      .catch(() => undefined);
  };

  return (
    <>
      <div className="grid gap-3">
        <PageHeader
          title={
            existing
              ? `编辑 ${kind} · ${existing.name}`
              : `创建 ${kind}${isNamespacedAuthorization(resource) ? ` · ${namespace}` : ""}`
          }
          onBack={onClose}
          backDisabled={mutation.isPending}
        />

        {existing ? null : (
          <FormSection title={SECTION_LABELS.basic} problem={problemIn("basic")}>
            <div className="grid gap-1.5">
              <Label htmlFor="rbac-name">名称</Label>
              <Input
                id="rbac-name"
                value={name}
                autoComplete="off"
                spellCheck={false}
                placeholder="例如 model-reader"
                onChange={(event) => setName(event.target.value)}
              />
              <span className="text-subtle-foreground text-xs">
                {resource === "serviceaccounts"
                  ? "合法的 DNS 子域名，最长 253 个字符；创建后不可修改"
                  : "最长 253 个字符，不能包含 / 和 %；创建后不可修改"}
              </span>
            </div>
          </FormSection>
        )}

        {resource === "serviceaccounts" ? (
          <FormSection title={SECTION_LABELS.token} problem={problemIn("token")}>
            <div className="grid gap-1.5">
              <Label htmlFor="rbac-automount">自动挂载 ServiceAccount Token</Label>
              <Select value={automount} onValueChange={setAutomount}>
                <SelectTrigger id="rbac-automount" className="w-56">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={AUTOMOUNT_DEFAULT}>使用 Kubernetes 默认</SelectItem>
                  <SelectItem value="true">自动挂载</SelectItem>
                  <SelectItem value="false">不自动挂载</SelectItem>
                </SelectContent>
              </Select>
              <span className="text-subtle-foreground text-xs">
                ZKE 不读取该 ServiceAccount 的 Token 与关联 Secret
              </span>
            </div>
          </FormSection>
        ) : null}

        {withRules ? (
          <FormSection
            title={SECTION_LABELS.rules}
            hint="逗号分隔；API 组留空表示 core 组。超出 Agent 权限的规则会被 Kubernetes 拒绝"
            problem={problemIn("rules")}
          >
            <RuleRows rows={rules} onChange={setRules} />
          </FormSection>
        ) : null}

        {binding ? (
          <>
            <FormSection title={SECTION_LABELS.role} problem={problemIn("role")}>
              {existing ? (
                <Alert tone="info">
                  roleRef 在 Kubernetes 中不可更改：当前为 {existing.role_ref?.kind}/
                  {existing.role_ref?.name}。要指向其他角色需要删除后重建。
                </Alert>
              ) : (
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="角色类型" htmlFor="rbac-role-kind">
                    <Select
                      value={roleKind}
                      onValueChange={(value) => setRoleKind(value as "Role" | "ClusterRole")}
                      disabled={resource === "clusterrolebindings"}
                    >
                      <SelectTrigger id="rbac-role-kind">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="Role">Role</SelectItem>
                        <SelectItem value="ClusterRole">ClusterRole</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field label="角色名称" htmlFor="rbac-role-name">
                    <Input
                      id="rbac-role-name"
                      value={roleName}
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => setRoleName(event.target.value)}
                    />
                  </Field>
                </div>
              )}
            </FormSection>

            <FormSection
              title={SECTION_LABELS.subjects}
              hint="至少一个；ServiceAccount 需要命名空间"
              problem={problemIn("subjects")}
            >
              <SubjectRows rows={subjects} onChange={setSubjects} defaultNamespace={namespace} />
            </FormSection>
          </>
        ) : null}

        {/*
         * The message itself is up in the section that can fix it; down here,
         * next to the button it disables, what is missing is where to look.
         */}
        {problem ? (
          <Alert tone="warning">「{SECTION_LABELS[problem.section]}」中还有需要修正的项。</Alert>
        ) : null}
        {mutation.error ? <Alert tone="danger">{errorMessage(mutation.error)}</Alert> : null}

        <div className="flex flex-wrap items-center justify-end gap-3 pb-2">
          <span className="text-subtle-foreground text-xs">
            目标：{clusterName}
            {isNamespacedAuthorization(resource) ? ` / ${namespace}` : "（集群级对象）"}
          </span>
          {existing ? (
            <span className="text-subtle-foreground text-xs">
              更新会整体替换{withRules ? "规则" : "主体"}列表：本次未提交的项将从对象中移除。
            </span>
          ) : null}
          <Button
            variant="primary"
            size="sm"
            disabled={problem !== null || mutation.isPending}
            onClick={() => submit(true, buildSpec())}
          >
            {mutation.isPending ? "预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={existing ? `确认更新 ${kind}` : `确认创建 ${kind}`}
        description="DryRun 已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(isNamespacedAuthorization(resource) ? [{ label: "命名空间", name: namespace }] : []),
          { label: kind, name: existing?.name ?? name.trim(), id: existing?.uid },
        ]}
        impacts={impacts(resource, existing !== null)}
        confirmLabel={existing ? "确认更新" : "确认创建"}
        destructive
        pending={mutation.isPending}
        error={mutation.error}
        onConfirm={() => previewed && submit(false, previewed)}
      />
    </>
  );
}

function impacts(resource: KubernetesAuthorizationResource, editing: boolean): string[] {
  const precondition =
    "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，更新会被拒绝而不是覆盖。";
  if (resource === "serviceaccounts") {
    return editing
      ? ["修改会影响使用该 ServiceAccount 的 Pod 下一次获取身份的方式。", precondition]
      : ["将创建一个新的集群内身份；它在被 RoleBinding 引用之前没有任何权限。"];
  }
  if (hasRules(resource)) {
    return editing
      ? [
          "规则会被整体替换：本次未提交的规则将从角色中移除，引用它的所有绑定立即受影响。",
          "权限变更即时生效，不需要重启工作负载。",
          precondition,
        ]
      : [
          "将创建一组权限定义；它在被绑定引用之前不授予任何主体权限。",
          "Agent 自身不持有 escalate 与 bind 权限，超出其权限范围的规则会被 Kubernetes 拒绝。",
        ];
  }
  return editing
    ? [
        "主体会被整体替换：本次未提交的主体将立即失去该绑定授予的权限。",
        "roleRef 不变；要指向其他角色需要删除后重建。",
        precondition,
      ]
    : [
        "将把角色中的权限授予所列主体，创建后立即生效。",
        "这会直接改变这些主体在集群中能做什么，请确认角色范围与主体身份都正确。",
      ];
}

/** Splits a comma-separated field; `keepEmpty` preserves the core API group. */
function splitList(value: string, keepEmpty = false): string[] {
  const parts = value.split(",").map((part) => part.trim());
  return keepEmpty
    ? parts.filter((part, index) => part !== "" || index === 0)
    : parts.filter(Boolean);
}

function RuleRows({
  rows,
  onChange,
}: {
  rows: RuleDraft[];
  onChange: (rows: RuleDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<RuleDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <div className="border-border/60 grid gap-2 rounded-md border p-2">
            <div className="grid gap-2 sm:grid-cols-2">
              <Input
                value={row.verbs}
                aria-label={`规则 ${index + 1} 动作`}
                placeholder="动作，例如 get, list, watch"
                autoComplete="off"
                onChange={(event) => update(index, { verbs: event.target.value })}
              />
              <Input
                value={row.apiGroups}
                aria-label={`规则 ${index + 1} API 组`}
                placeholder="API 组，留空为 core"
                autoComplete="off"
                onChange={(event) => update(index, { apiGroups: event.target.value })}
              />
              <Input
                value={row.resources}
                aria-label={`规则 ${index + 1} 资源`}
                placeholder="资源，例如 pods, pods/log"
                autoComplete="off"
                onChange={(event) => update(index, { resources: event.target.value })}
              />
              <Input
                value={row.resourceNames}
                aria-label={`规则 ${index + 1} 资源名称`}
                placeholder="限定名称，可留空"
                autoComplete="off"
                onChange={(event) => update(index, { resourceNames: event.target.value })}
              />
            </div>
            <Input
              value={row.nonResourceUrls}
              aria-label={`规则 ${index + 1} 非资源路径`}
              placeholder="非资源路径，例如 /healthz（仅 ClusterRole）"
              autoComplete="off"
              onChange={(event) => update(index, { nonResourceUrls: event.target.value })}
            />
          </div>
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`移除规则 ${index + 1}`}
            disabled={rows.length <= 1}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          >
            <X />
          </Button>
        </div>
      ))}
      <div>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => onChange([...rows, { ...EMPTY_RULE }])}
        >
          <Plus />
          添加规则
        </Button>
      </div>
    </div>
  );
}

function SubjectRows({
  rows,
  onChange,
  defaultNamespace,
}: {
  rows: SubjectDraft[];
  onChange: (rows: SubjectDraft[]) => void;
  defaultNamespace: string;
}) {
  const update = (index: number, patch: Partial<SubjectDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[10rem_1fr_1fr_auto] items-center gap-2">
          <Select
            value={row.kind}
            onValueChange={(value) => update(index, { kind: value as SubjectDraft["kind"] })}
          >
            <SelectTrigger aria-label={`主体 ${index + 1} 类型`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="ServiceAccount">ServiceAccount</SelectItem>
              <SelectItem value="User">User</SelectItem>
              <SelectItem value="Group">Group</SelectItem>
            </SelectContent>
          </Select>
          <Input
            value={row.name}
            aria-label={`主体 ${index + 1} 名称`}
            placeholder="名称"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => update(index, { name: event.target.value })}
          />
          <Input
            value={row.namespace}
            aria-label={`主体 ${index + 1} 命名空间`}
            placeholder="命名空间"
            autoComplete="off"
            spellCheck={false}
            disabled={row.kind !== "ServiceAccount"}
            onChange={(event) => update(index, { namespace: event.target.value })}
          />
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`移除主体 ${index + 1}`}
            disabled={rows.length <= 1}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
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
            onChange([...rows, { kind: "ServiceAccount", name: "", namespace: defaultNamespace }])
          }
        >
          <Plus />
          添加主体
        </Button>
      </div>
    </div>
  );
}

function FormSection({
  title,
  hint,
  problem,
  children,
}: {
  title: string;
  hint?: string;
  /** The current blocking problem, when it is this section that carries it. */
  problem?: string;
  children: ReactNode;
}) {
  return (
    <section>
      <div className="mb-2 flex flex-wrap items-baseline gap-2">
        <h4 className="text-foreground text-[13px] font-medium">{title}</h4>
        {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
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
  children,
}: {
  label: string;
  htmlFor: string;
  children: ReactNode;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
    </div>
  );
}
