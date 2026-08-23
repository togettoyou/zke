import { useId, useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import {
  useCreateNetworkingResource,
  useUpdateNetworkingResource,
  type NetworkingSummary,
  type NetworkingSpecInput,
} from "@/api/queries/networking";
import type { KubernetesNetworkingResource } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
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

import { isGatewayRouteResource, networkingKindLabel } from "./networking-catalog";
import { GatewayRouteEditor } from "./GatewayRouteEditor";
import {
  buildNetworkingSpec,
  createDraft,
  DEFAULT_OPTION,
  emptyListener,
  emptyPath,
  emptyPort,
  emptyRule,
  emptyTls,
  initialDraft,
  networkingProblem,
  nodePortWarnings,
  PORT_DIGITS,
  PORT_RANGE,
  USUAL_NODE_PORT_RANGE,
  type GatewayDraft,
  type IngressDraft,
  type ListenerDraft,
  type NetworkingDraft,
  type NetworkingSectionKey,
  type PairDraft,
  type ServiceDraft,
} from "./networking-form-model";

/** The titles the sections are rendered with, to point at one from elsewhere. */
const SECTION_LABELS: Record<NetworkingSectionKey, string> = {
  basic: "基本信息",
  service: "Service",
  ports: "端口",
  selector: "选择器",
  ingress: "Ingress",
  rules: "转发规则",
  tls: "TLS",
  gateway: "Gateway",
  listeners: "监听器",
  route: "Route 配置",
};

/**
 * Creates or replaces one Service, Ingress or Gateway.
 *
 * A page rather than a dialog, for the same reason the workload form is one: a
 * Gateway with three listeners or an Ingress with a handful of routes is taller
 * than a box laid over the list can show, and the list underneath is of no use
 * while it is being filled in.
 *
 * Update sends the object's UID and resourceVersion, which the Server checks
 * against a fresh read: an edit of a stale view is refused rather than applied
 * over whatever changed in between. Fields this form does not model — a
 * Service's assigned ClusterIP and NodePorts, a Gateway's CRD extensions — are
 * preserved by the Server, so leaving them out of the form does not remove them
 * from the object.
 */
export function NetworkingFormView({
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
  resource: KubernetesNetworkingResource;
  /**
   * Set when editing; absent when creating. A summary is enough — the form
   * needs the identity and the typed spec, not the annotations.
   */
  existing: NetworkingSummary | null;
  onClose: () => void;
}) {
  const create = useCreateNetworkingResource();
  const update = useUpdateNetworkingResource();
  const mutation = existing ? update : create;
  const kind = networkingKindLabel(resource);
  const [previewed, setPreviewed] = useState<NetworkingSpecInput | null>(null);
  const previewKey = useSubmissionKey(previewed === null);
  const applyKey = useSubmissionKey(previewed !== null);
  const [draft, setDraft] = useState<NetworkingDraft>(() =>
    existing ? initialDraft(existing) : createDraft(resource),
  );

  const patch = (changes: Partial<NetworkingDraft>) =>
    setDraft((current) => ({ ...current, ...changes }));
  const problem = networkingProblem(draft, resource, existing !== null);
  const problemIn = (section: NetworkingSectionKey) =>
    problem?.section === section ? problem.message : undefined;

  const submit = (dryRun: boolean, spec: NetworkingSpecInput) => {
    const shared = {
      clusterId,
      namespace,
      resource,
      spec,
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    const request = existing
      ? update.mutateAsync({
          ...shared,
          name: existing.name,
          uid: existing.uid,
          resourceVersion: existing.resource_version,
        })
      : create.mutateAsync({ ...shared, name: draft.name.trim() });
    void request
      .then(() => {
        if (dryRun) {
          setPreviewed(spec);
          return;
        }
        toast.success(
          `${kind} ${existing?.name ?? draft.name.trim()} 已${existing ? "更新" : "创建"}`,
        );
        onClose();
      })
      .catch(() => undefined);
  };

  return (
    <>
      <div className="grid gap-3">
        <PageHeader
          title={existing ? `编辑 ${kind} · ${existing.name}` : `创建 ${kind} · ${namespace}`}
          onBack={onClose}
        />

        {existing ? null : (
          <FormSection title={SECTION_LABELS.basic} problem={problemIn("basic")}>
            <div className="grid gap-3 @md:grid-cols-2">
              <Field
                label="名称"
                hint={
                  resource === "services"
                    ? "小写字母开头，最长 63 个字符；创建后不可修改"
                    : "合法的 DNS 子域名，最长 253 个字符；创建后不可修改"
                }
              >
                {(id) => (
                  <Input
                    id={id}
                    value={draft.name}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="例如 model-gateway"
                    onChange={(event) => patch({ name: event.target.value })}
                  />
                )}
              </Field>
            </div>
          </FormSection>
        )}

        {resource === "services" ? (
          <ServiceFields
            draft={draft.service}
            existing={existing}
            onChange={(service) => patch({ service })}
            problemIn={problemIn}
          />
        ) : null}
        {resource === "ingresses" ? (
          <IngressFields
            draft={draft.ingress}
            onChange={(ingress) => patch({ ingress })}
            problemIn={problemIn}
          />
        ) : null}
        {resource === "gateways" ? (
          <GatewayFields
            draft={draft.gateway}
            onChange={(gateway) => patch({ gateway })}
            problemIn={problemIn}
          />
        ) : null}
        {isGatewayRouteResource(resource) ? (
          <FormSection
            title={SECTION_LABELS.route}
            hint="按 Gateway API 原生语义配置；跨命名空间引用必须由目标 Gateway/ReferenceGrant 授权"
            problem={problemIn("route")}
          >
            <GatewayRouteEditor
              resource={resource}
              draft={draft.gatewayRoute}
              onChange={(gatewayRoute) => patch({ gatewayRoute })}
            />
          </FormSection>
        ) : null}

        {/*
         * The message itself is up in the section that can fix it; down here,
         * next to the button it disables, what is missing is where to look.
         */}
        {problem ? (
          <Alert tone="warning">「{SECTION_LABELS[problem.section]}」中还有需要修正的项。</Alert>
        ) : null}
        {mutation.error ? <Alert tone="danger">{errorMessage(mutation.error)}</Alert> : null}

        <div className="flex items-center justify-end gap-3 pb-2">
          <span className="text-subtle-foreground text-xs">
            目标：{clusterName} / {namespace}
          </span>
          <Button
            variant="primary"
            size="sm"
            disabled={problem !== null || mutation.isPending}
            onClick={() => submit(true, buildNetworkingSpec(draft, resource, existing))}
          >
            {mutation.isPending ? "DryRun 预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={existing ? `确认更新 ${kind}` : `确认创建 ${kind}`}
        description="DryRun 预检已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: kind, name: existing?.name ?? draft.name.trim(), id: existing?.uid },
        ]}
        impacts={
          existing
            ? [
                "本表单建模的配置会整体替换现有配置；Kubernetes 分配的字段和未建模的扩展字段会按该资源的更新语义保留。",
                "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，更新会被拒绝而不是覆盖。",
                "变更可能立即改变流量走向。",
              ]
            : [
                "将在目标集群持久化一个新对象，并交由 Kubernetes 及相关 Controller 处理；实际流量是否可达取决于端点和 Controller 状态。",
              ]
        }
        confirmLabel={existing ? "确认更新" : "确认创建"}
        destructive={existing !== null}
        pending={mutation.isPending}
        error={mutation.error}
        onConfirm={() => previewed && submit(false, previewed)}
      />
    </>
  );
}

type ProblemLookup = (section: NetworkingSectionKey) => string | undefined;

function ServiceFields({
  draft,
  existing,
  onChange,
  problemIn,
}: {
  draft: ServiceDraft;
  existing: NetworkingSummary | null;
  onChange: (draft: ServiceDraft) => void;
  problemIn: ProblemLookup;
}) {
  const view = existing?.service;
  const patch = (changes: Partial<ServiceDraft>) => onChange({ ...draft, ...changes });
  const externalNameService = draft.type === "ExternalName";
  const exposesNodePort = draft.type === "NodePort" || draft.type === "LoadBalancer";
  // Kubernetes settles a Service's headless identity at creation and refuses to
  // change it afterwards, so an existing non-ExternalName Service cannot switch.
  const headlessLocked = view !== undefined && view.spec.type !== "ExternalName";
  // NodePort and LoadBalancer are both built on a ClusterIP, and a Service
  // created headless has none — `spec.clusterIPs` is immutable, so Kubernetes
  // will not allocate one now. The two entries are disabled for that reason,
  // and the reason is written here: a greyed-out row in a dropdown states the
  // conclusion and none of the why.
  const headlessService = view?.spec.headless === true;

  return (
    <>
      <FormSection title={SECTION_LABELS.service} problem={problemIn("service")}>
        <div className="grid gap-3 @md:grid-cols-2">
          <Field
            label="类型"
            hint={
              headlessService
                ? "该 Service 创建时即为 headless，没有 ClusterIP，Kubernetes 也不会再为它分配，因此不能改为 NodePort 或 LoadBalancer；需要这两种类型请另建一个 Service。"
                : undefined
            }
          >
            {(id) => (
              <Select value={draft.type} onValueChange={(type) => patch({ type })}>
                <SelectTrigger id={id}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ClusterIP">ClusterIP</SelectItem>
                  <SelectItem value="NodePort" disabled={headlessService}>
                    NodePort
                  </SelectItem>
                  <SelectItem value="LoadBalancer" disabled={headlessService}>
                    LoadBalancer
                  </SelectItem>
                  <SelectItem value="ExternalName">ExternalName</SelectItem>
                </SelectContent>
              </Select>
            )}
          </Field>
          {externalNameService ? (
            <Field label="ExternalName" hint="Service 会被解析成这个域名的 CNAME">
              {(id) => (
                <Input
                  id={id}
                  value={draft.externalName}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="例如 example.com"
                  onChange={(event) => patch({ externalName: event.target.value })}
                />
              )}
            </Field>
          ) : (
            <Field label="会话保持">
              {(id) => (
                <Select
                  value={draft.sessionAffinity}
                  onValueChange={(sessionAffinity) => patch({ sessionAffinity })}
                >
                  <SelectTrigger id={id}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={DEFAULT_OPTION}>默认（None）</SelectItem>
                    <SelectItem value="None">None</SelectItem>
                    <SelectItem value="ClientIP">ClientIP</SelectItem>
                  </SelectContent>
                </Select>
              )}
            </Field>
          )}
          {exposesNodePort ? (
            <Field label="外部流量策略" hint="Local 保留客户端源地址，但只转发到本节点上的 Pod">
              {(id) => (
                <Select
                  value={draft.externalPolicy}
                  onValueChange={(externalPolicy) => patch({ externalPolicy })}
                >
                  <SelectTrigger id={id}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={DEFAULT_OPTION}>默认（Cluster）</SelectItem>
                    <SelectItem value="Cluster">Cluster</SelectItem>
                    <SelectItem value="Local">Local</SelectItem>
                  </SelectContent>
                </Select>
              )}
            </Field>
          ) : null}
        </div>
        {draft.type === "ClusterIP" ? (
          <label className="mt-3 flex items-center gap-2 text-[13px]">
            <Checkbox
              checked={draft.headless}
              disabled={headlessLocked}
              onCheckedChange={(checked) => patch({ headless: checked === true })}
            />
            Headless（不分配 ClusterIP，DNS 直接解析到 Pod）
            {headlessLocked ? "；创建后不可切换" : ""}
          </label>
        ) : null}
      </FormSection>

      {externalNameService ? null : (
        <>
          <FormSection
            title={SECTION_LABELS.ports}
            hint={`至少一个；端口取 ${PORT_RANGE}，目标端口留空表示与端口相同，也可以写容器端口的名称`}
            problem={problemIn("ports")}
          >
            <RowList
              rows={draft.ports}
              onChange={(ports) => patch({ ports })}
              addLabel="添加端口"
              create={emptyPort}
              minimum={1}
              render={(row, index, update) => (
                <div className="grid grid-cols-[1fr_6rem_7rem_7rem_7rem] gap-2">
                  <Input
                    value={row.name}
                    aria-label={`端口 ${index + 1} 名称`}
                    placeholder={draft.ports.length > 1 ? "名称（必填）" : "名称"}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => update({ name: event.target.value })}
                  />
                  {/*
                   * A port is at most five digits, so a sixth is not a value
                   * to be rejected later — it is a keystroke that cannot lead
                   * anywhere. The 1–65535 bound itself still comes from the
                   * validator: 99999 is five digits and out of range.
                   */}
                  <NumericInput
                    value={row.port}
                    aria-label={`端口 ${index + 1} 端口`}
                    placeholder="端口"
                    maxLength={PORT_DIGITS}
                    onValueChange={(port) => update({ port })}
                  />
                  {/* Not numeric-only: a target port may also name a container port. */}
                  <Input
                    value={row.targetPort}
                    aria-label={`端口 ${index + 1} 目标端口`}
                    placeholder="目标端口"
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => update({ targetPort: event.target.value })}
                  />
                  <Select value={row.protocol} onValueChange={(protocol) => update({ protocol })}>
                    <SelectTrigger aria-label={`端口 ${index + 1} 协议`}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={DEFAULT_OPTION}>默认 TCP</SelectItem>
                      <SelectItem value="TCP">TCP</SelectItem>
                      <SelectItem value="UDP">UDP</SelectItem>
                      <SelectItem value="SCTP">SCTP</SelectItem>
                    </SelectContent>
                  </Select>
                  <NumericInput
                    value={row.nodePort}
                    aria-label={`端口 ${index + 1} NodePort`}
                    placeholder="NodePort"
                    maxLength={PORT_DIGITS}
                    disabled={!exposesNodePort}
                    title={
                      exposesNodePort
                        ? undefined
                        : "只有 NodePort 和 LoadBalancer 类型会分配 NodePort"
                    }
                    onValueChange={(nodePort) => update({ nodePort })}
                  />
                </div>
              )}
            />
            {exposesNodePort ? (
              <p className="text-subtle-foreground mt-2 text-xs">
                NodePort 留空由 Kubernetes 分配，通常在 {USUAL_NODE_PORT_RANGE} 范围内。
              </p>
            ) : null}
            {nodePortWarnings(draft).map((warning) => (
              <Alert key={warning} tone="warning" className="mt-2">
                {warning}
              </Alert>
            ))}
          </FormSection>

          <FormSection
            title={SECTION_LABELS.selector}
            hint="按标签选中要转发到的 Pod；留空表示自行管理 Endpoints"
            problem={problemIn("selector")}
          >
            <PairList
              rows={draft.selector}
              onChange={(selector) => patch({ selector })}
              addLabel="添加选择器"
            />
          </FormSection>
        </>
      )}
    </>
  );
}

function IngressFields({
  draft,
  onChange,
  problemIn,
}: {
  draft: IngressDraft;
  onChange: (draft: IngressDraft) => void;
  problemIn: ProblemLookup;
}) {
  const patch = (changes: Partial<IngressDraft>) => onChange({ ...draft, ...changes });
  return (
    <>
      <FormSection title={SECTION_LABELS.ingress} problem={problemIn("ingress")}>
        <div className="grid gap-3">
          <Field label="IngressClass" hint="留空使用集群默认 IngressClass">
            {(id) => (
              <Input
                id={id}
                value={draft.className}
                autoComplete="off"
                spellCheck={false}
                placeholder="例如 nginx"
                onChange={(event) => patch({ className: event.target.value })}
              />
            )}
          </Field>
          <label className="flex items-center gap-2 text-[13px]">
            <Checkbox
              checked={draft.defaultBackend.enabled}
              onCheckedChange={(checked) =>
                patch({
                  defaultBackend: { ...draft.defaultBackend, enabled: checked === true },
                })
              }
            />
            配置默认后端（接收没有匹配到任何规则的请求）
          </label>
          {draft.defaultBackend.enabled ? (
            <div className="grid grid-cols-[1fr_10rem] gap-2">
              <Input
                value={draft.defaultBackend.name}
                aria-label="默认后端 Service"
                placeholder="Service 名称"
                autoComplete="off"
                spellCheck={false}
                onChange={(event) =>
                  patch({
                    defaultBackend: { ...draft.defaultBackend, name: event.target.value },
                  })
                }
              />
              <Input
                value={draft.defaultBackend.port}
                aria-label="默认后端端口"
                placeholder="端口或名称"
                autoComplete="off"
                spellCheck={false}
                onChange={(event) =>
                  patch({
                    defaultBackend: { ...draft.defaultBackend, port: event.target.value },
                  })
                }
              />
            </div>
          ) : null}
        </div>
      </FormSection>

      <FormSection
        title={SECTION_LABELS.rules}
        hint="默认后端和规则至少配置一项；主机留空表示任意主机"
        problem={problemIn("rules")}
      >
        <RowList
          rows={draft.rules}
          onChange={(rules) => patch({ rules })}
          addLabel="添加规则"
          create={emptyRule}
          render={(rule, ruleIndex, updateRule) => (
            <div className="border-border/60 rounded-control grid gap-2 border p-2">
              <Input
                value={rule.host}
                aria-label={`规则 ${ruleIndex + 1} 主机`}
                placeholder="主机，例如 api.example.com；留空匹配任意主机"
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => updateRule({ host: event.target.value })}
              />
              <RowList
                rows={rule.paths}
                onChange={(paths) => updateRule({ paths })}
                addLabel="添加路径"
                minimum={1}
                create={emptyPath}
                render={(path, pathIndex, updatePath) => (
                  <div className="grid grid-cols-[1fr_9rem_1fr_7rem] gap-2">
                    <Input
                      value={path.path}
                      aria-label={`规则 ${ruleIndex + 1} 路径 ${pathIndex + 1}`}
                      placeholder="路径，例如 /api"
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => updatePath({ path: event.target.value })}
                    />
                    <Select
                      value={path.pathType}
                      onValueChange={(pathType) => updatePath({ pathType })}
                    >
                      <SelectTrigger
                        aria-label={`规则 ${ruleIndex + 1} 路径 ${pathIndex + 1} 匹配方式`}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="Prefix">Prefix</SelectItem>
                        <SelectItem value="Exact">Exact</SelectItem>
                        <SelectItem value="ImplementationSpecific">
                          ImplementationSpecific
                        </SelectItem>
                      </SelectContent>
                    </Select>
                    <Input
                      value={path.backendName}
                      aria-label={`规则 ${ruleIndex + 1} 路径 ${pathIndex + 1} 后端 Service`}
                      placeholder="后端 Service"
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => updatePath({ backendName: event.target.value })}
                    />
                    <Input
                      value={path.backendPort}
                      aria-label={`规则 ${ruleIndex + 1} 路径 ${pathIndex + 1} 后端端口`}
                      placeholder="端口或名称"
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => updatePath({ backendPort: event.target.value })}
                    />
                  </div>
                )}
              />
            </div>
          )}
        />
      </FormSection>

      <FormSection
        title={SECTION_LABELS.tls}
        hint="可选；证书取同一命名空间中的 Secret，多个主机用逗号分隔"
        problem={problemIn("tls")}
      >
        <RowList
          rows={draft.tls}
          onChange={(tls) => patch({ tls })}
          addLabel="添加 TLS"
          create={emptyTls}
          render={(row, index, update) => (
            <div className="grid grid-cols-[1fr_1fr] gap-2">
              <Input
                value={row.hosts}
                aria-label={`TLS ${index + 1} 主机`}
                placeholder="主机，逗号分隔"
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => update({ hosts: event.target.value })}
              />
              <Input
                value={row.secretName}
                aria-label={`TLS ${index + 1} Secret`}
                placeholder="Secret 名称"
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => update({ secretName: event.target.value })}
              />
            </div>
          )}
        />
      </FormSection>
    </>
  );
}

function GatewayFields({
  draft,
  onChange,
  problemIn,
}: {
  draft: GatewayDraft;
  onChange: (draft: GatewayDraft) => void;
  problemIn: ProblemLookup;
}) {
  const patch = (changes: Partial<GatewayDraft>) => onChange({ ...draft, ...changes });
  return (
    <>
      <FormSection title={SECTION_LABELS.gateway} problem={problemIn("gateway")}>
        <div className="grid gap-3 @md:grid-cols-2">
          <Field label="GatewayClass" hint="必须是集群中已存在的 GatewayClass">
            {(id) => (
              <Input
                id={id}
                value={draft.className}
                autoComplete="off"
                spellCheck={false}
                placeholder="例如 istio"
                onChange={(event) => patch({ className: event.target.value })}
              />
            )}
          </Field>
        </div>
      </FormSection>

      <FormSection
        title={SECTION_LABELS.listeners}
        hint={`至少一个；端口取 ${PORT_RANGE}，TLS 证书取同一命名空间中的 Secret`}
        problem={problemIn("listeners")}
      >
        <RowList
          rows={draft.listeners}
          onChange={(listeners) => patch({ listeners })}
          addLabel="添加监听器"
          create={emptyListener}
          minimum={1}
          render={(listener, index, update) => (
            <ListenerRow listener={listener} index={index} update={update} />
          )}
        />
      </FormSection>
    </>
  );
}

function ListenerRow({
  listener,
  index,
  update,
}: {
  listener: ListenerDraft;
  index: number;
  update: (patch: Partial<ListenerDraft>) => void;
}) {
  const tlsCapable = listener.protocol === "HTTPS" || listener.protocol === "TLS";
  return (
    <div className="border-border/60 rounded-control grid gap-2 border p-2">
      <div className="grid grid-cols-[1fr_1fr_6rem_7rem] gap-2">
        <Input
          value={listener.name}
          aria-label={`监听器 ${index + 1} 名称`}
          placeholder="名称"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ name: event.target.value })}
        />
        <Input
          value={listener.hostname}
          aria-label={`监听器 ${index + 1} 主机`}
          placeholder="主机，可留空"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ hostname: event.target.value })}
        />
        <NumericInput
          value={listener.port}
          aria-label={`监听器 ${index + 1} 端口`}
          placeholder="端口"
          maxLength={PORT_DIGITS}
          onValueChange={(port) => update({ port })}
        />
        <Select
          value={listener.protocol}
          onValueChange={(protocol) =>
            update({
              protocol,
              // A protocol that cannot terminate TLS must not carry a mode or a
              // certificate: the Server refuses the combination outright.
              ...(protocol === "HTTPS"
                ? { tlsMode: "Terminate" }
                : protocol === "TLS"
                  ? {}
                  : { tlsMode: DEFAULT_OPTION, certificateSecrets: "" }),
            })
          }
        >
          <SelectTrigger aria-label={`监听器 ${index + 1} 协议`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {["HTTP", "HTTPS", "TLS", "TCP", "UDP"].map((protocol) => (
              <SelectItem key={protocol} value={protocol}>
                {protocol}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="grid grid-cols-[9rem_1fr_9rem] gap-2">
        <Select
          value={listener.tlsMode}
          onValueChange={(tlsMode) =>
            update({ tlsMode, ...(tlsMode === "Passthrough" ? { certificateSecrets: "" } : {}) })
          }
          disabled={!tlsCapable}
        >
          <SelectTrigger aria-label={`监听器 ${index + 1} TLS 模式`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={DEFAULT_OPTION}>选择 TLS 模式</SelectItem>
            <SelectItem value="Terminate">Terminate</SelectItem>
            {listener.protocol === "TLS" ? (
              <SelectItem value="Passthrough">Passthrough</SelectItem>
            ) : null}
          </SelectContent>
        </Select>
        <Input
          value={listener.certificateSecrets}
          aria-label={`监听器 ${index + 1} 证书 Secret`}
          placeholder="Secret 名称，逗号分隔"
          autoComplete="off"
          spellCheck={false}
          disabled={listener.tlsMode !== "Terminate"}
          onChange={(event) => update({ certificateSecrets: event.target.value })}
        />
        <Select
          value={listener.namespacesFrom}
          onValueChange={(namespacesFrom) => update({ namespacesFrom })}
        >
          <SelectTrigger aria-label={`监听器 ${index + 1} 允许的 Route 来源`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={DEFAULT_OPTION}>默认（Same）</SelectItem>
            <SelectItem value="Same">Same</SelectItem>
            <SelectItem value="All">All</SelectItem>
            {listener.namespacesFrom === "Selector" ? (
              <SelectItem value="Selector">Selector（保留现有规则）</SelectItem>
            ) : null}
          </SelectContent>
        </Select>
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
    <section className="border-border bg-surface rounded-panel border p-3">
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
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: (id: string) => ReactNode;
}) {
  const id = useId();
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children(id)}
      {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
    </div>
  );
}

/** A repeatable list of rows, each rendered by its owner. */
function RowList<T>({
  rows,
  onChange,
  addLabel,
  create,
  render,
  minimum = 0,
}: {
  rows: T[];
  onChange: (rows: T[]) => void;
  addLabel: string;
  create: () => T;
  render: (row: T, index: number, update: (patch: Partial<T>) => void) => ReactNode;
  minimum?: number;
}) {
  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          {render(row, index, (patch) =>
            onChange(
              rows.map((item, position) => (position === index ? { ...item, ...patch } : item)),
            ),
          )}
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`移除第 ${index + 1} 项`}
            disabled={rows.length <= minimum}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          >
            <X />
          </Button>
        </div>
      ))}
      <div>
        <Button size="sm" variant="secondary" onClick={() => onChange([...rows, create()])}>
          <Plus />
          {addLabel}
        </Button>
      </div>
    </div>
  );
}

function PairList({
  rows,
  onChange,
  addLabel,
}: {
  rows: PairDraft[];
  onChange: (rows: PairDraft[]) => void;
  addLabel: string;
}) {
  return (
    <RowList
      rows={rows}
      onChange={onChange}
      addLabel={addLabel}
      create={() => ({ key: "", value: "" })}
      render={(row, index, update) => (
        <div className="grid grid-cols-[1fr_1fr] gap-2">
          <Input
            value={row.key}
            aria-label={`第 ${index + 1} 项键`}
            placeholder="键"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => update({ key: event.target.value })}
          />
          <Input
            value={row.value}
            aria-label={`第 ${index + 1} 项值`}
            placeholder="值"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => update({ value: event.target.value })}
          />
        </div>
      )}
    />
  );
}
