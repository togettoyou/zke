import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import {
  useCreateNetworkingResource,
  useUpdateNetworkingResource,
  type NetworkingSummary,
  type NetworkingSpecInput,
} from "@/api/queries/networking";
import type { KubernetesGatewaySpecInput, KubernetesNetworkingResource } from "@/api/types";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
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

import { networkingKindLabel } from "./networking-catalog";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const DNS1035_LABEL = /^[a-z]([-a-z0-9]*[a-z0-9])?$/;
const PORT_NAME = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
/** Radix Select cannot hold an empty value, so "leave it to Kubernetes" needs a name. */
const DEFAULT_OPTION = "__default__";

type PortDraft = {
  name: string;
  port: string;
  targetPort: string;
  protocol: string;
  nodePort: string;
};
type PairDraft = { key: string; value: string };
type PathDraft = { path: string; pathType: string; backendName: string; backendPort: string };
type RuleDraft = { host: string; paths: PathDraft[] };
type TlsDraft = { hosts: string; secretName: string };
type DefaultBackendDraft = { enabled: boolean; name: string; port: string };
type ListenerDraft = {
  originalName: string | null;
  name: string;
  hostname: string;
  port: string;
  protocol: string;
  tlsMode: string;
  certificateSecrets: string;
  namespacesFrom: string;
};

/**
 * Creates or replaces one Service, Ingress or Gateway.
 *
 * Update sends the object's UID and resourceVersion, which the Server checks
 * against a fresh read: an edit of a stale view is refused rather than applied
 * over whatever changed in between. Fields this form does not model — a
 * Service's assigned ClusterIP and NodePorts, a Gateway's CRD extensions — are
 * preserved by the Server, so leaving them out of the form does not remove them
 * from the object.
 */
export function NetworkingForm({
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

  const [name, setName] = useState(existing?.name ?? "");
  const service = useServiceDraft(existing);
  const ingress = useIngressDraft(existing);
  const gateway = useGatewayDraft(existing);

  const editor = resource === "services" ? service : resource === "ingresses" ? ingress : gateway;
  const trimmedName = name.trim();
  const nameValid =
    existing !== null ||
    (resource === "services"
      ? DNS1035_LABEL.test(trimmedName) && trimmedName.length <= 63
      : DNS_SUBDOMAIN.test(trimmedName) && trimmedName.length <= 253);
  const valid = nameValid && editor.valid;

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
      : create.mutateAsync({ ...shared, name: name.trim() });
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
      <Dialog open={previewed === null} onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined} className="w-[min(760px,calc(100vw-2rem))]">
          <DialogHeader>
            <DialogTitle>
              {existing ? `编辑 ${kind}` : `创建 ${kind}`}
              {existing ? ` · ${existing.name}` : ""}
            </DialogTitle>
            <DialogDescription>
              第一步只执行服务端 DryRun，不会在集群中写入任何变更。
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-4">
            {existing ? null : (
              <FormSection title="基本信息">
                <div className="grid gap-1.5">
                  <Label htmlFor="networking-name">名称</Label>
                  <Input
                    id="networking-name"
                    value={name}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="例如 model-gateway"
                    onChange={(event) => setName(event.target.value)}
                  />
                </div>
              </FormSection>
            )}
            {editor.fields}
          </div>

          <Alert tone="info" className="mt-4">
            目标：{clusterName} / {namespace}
          </Alert>
          {mutation.error ? (
            <Alert tone="danger" className="mt-3">
              {errorMessage(mutation.error)}
            </Alert>
          ) : null}

          <DialogFooter>
            <Button variant="ghost" onClick={onClose} disabled={mutation.isPending}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={!valid || mutation.isPending}
              onClick={() => submit(true, editor.build())}
            >
              {mutation.isPending ? "预检中…" : "执行 DryRun 预检"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={existing ? `确认更新 ${kind}` : `确认创建 ${kind}`}
        description="DryRun 已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: kind, name: existing?.name ?? name.trim(), id: existing?.uid },
        ]}
        impacts={
          existing
            ? [
                "本表单建模的配置会整体替换现有配置；Kubernetes 分配的字段和未建模的扩展字段由服务端保留。",
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

/** What each per-type editor gives the shared form shell. */
type SpecEditor = {
  fields: ReactNode;
  valid: boolean;
  build: () => NetworkingSpecInput;
};

function useServiceDraft(existing: NetworkingSummary | null): SpecEditor {
  const view = existing?.service;
  const [type, setType] = useState<string>(view?.spec.type || "ClusterIP");
  const [headless, setHeadless] = useState(view?.spec.headless ?? false);
  const [externalName, setExternalName] = useState(view?.spec.external_name ?? "");
  const [sessionAffinity, setSessionAffinity] = useState(
    view?.spec.session_affinity || DEFAULT_OPTION,
  );
  const [externalPolicy, setExternalPolicy] = useState(
    view?.spec.external_traffic_policy || DEFAULT_OPTION,
  );
  const [selector, setSelector] = useState<PairDraft[]>(
    Object.entries(view?.spec.selector ?? {}).map(([key, value]) => ({ key, value })),
  );
  const [ports, setPorts] = useState<PortDraft[]>(
    (view?.spec.ports ?? []).map((port) => ({
      name: port.name,
      port: String(port.port),
      targetPort: port.target_port,
      protocol: port.protocol || DEFAULT_OPTION,
      nodePort: port.node_port ? String(port.node_port) : "",
    })),
  );

  const externalNameService = type === "ExternalName";
  const exposesNodePort = type === "NodePort" || type === "LoadBalancer";
  const headlessLocked = view !== undefined && view.spec.type !== "ExternalName";
  const portNames = ports.map((port) => port.name.trim()).filter(Boolean);
  const portKeys = ports.map(
    (port) => `${port.protocol === DEFAULT_OPTION ? "TCP" : port.protocol}/${port.port.trim()}`,
  );
  const portsValid =
    ports.every((port) => {
      const name = port.name.trim();
      const targetPort = port.targetPort.trim();
      return (
        validPortNumber(port.port) &&
        (name === "" || validNamedPort(name)) &&
        (targetPort === "" || validPortNumber(targetPort) || validNamedPort(targetPort)) &&
        (!exposesNodePort || port.nodePort.trim() === "" || validPortNumber(port.nodePort))
      );
    }) &&
    (ports.length <= 1 || ports.every((port) => port.name.trim() !== "")) &&
    new Set(portNames).size === portNames.length &&
    new Set(portKeys).size === portKeys.length;
  const valid = externalNameService
    ? DNS_SUBDOMAIN.test(externalName.trim()) && externalName.trim().length <= 253
    : portsValid &&
      ports.length > 0 &&
      selector.every(
        (pair) =>
          (pair.key.trim() === "" && pair.value.trim() === "") ||
          (pair.key.trim() !== "" && pair.value.trim() !== ""),
      );

  const build = (): NetworkingSpecInput => ({
    service: {
      type: type as "ClusterIP" | "NodePort" | "LoadBalancer" | "ExternalName",
      headless: type === "ClusterIP" ? headless : false,
      selector: externalNameService
        ? {}
        : Object.fromEntries(
            selector
              .filter((pair) => pair.key.trim() !== "")
              .map((pair) => [pair.key.trim(), pair.value.trim()]),
          ),
      ports: externalNameService
        ? []
        : ports.map((port) => ({
            ...(port.name.trim() ? { name: port.name.trim() } : {}),
            port: Number(port.port.trim()),
            ...(port.targetPort.trim() ? { target_port: port.targetPort.trim() } : {}),
            ...(port.protocol === DEFAULT_OPTION
              ? {}
              : { protocol: port.protocol as "TCP" | "UDP" | "SCTP" }),
            ...(exposesNodePort && port.nodePort.trim()
              ? { node_port: Number(port.nodePort.trim()) }
              : {}),
          })),
      ...(externalNameService ? { external_name: externalName.trim() } : {}),
      ...(!externalNameService && sessionAffinity !== DEFAULT_OPTION
        ? { session_affinity: sessionAffinity as "None" | "ClientIP" }
        : {}),
      ...(exposesNodePort && externalPolicy !== DEFAULT_OPTION
        ? { external_traffic_policy: externalPolicy as "Cluster" | "Local" }
        : {}),
      ...(!externalNameService && view?.spec.internal_traffic_policy
        ? { internal_traffic_policy: view.spec.internal_traffic_policy }
        : {}),
      ...(!externalNameService && view?.spec.publish_not_ready_addresses
        ? { publish_not_ready_addresses: true }
        : {}),
      ...(type === "LoadBalancer" && view?.spec.allocate_load_balancer_node_ports !== undefined
        ? { allocate_load_balancer_node_ports: view.spec.allocate_load_balancer_node_ports }
        : {}),
    },
  });

  const fields = (
    <>
      <FormSection title="Service">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="类型" htmlFor="service-type">
            <Select value={type} onValueChange={setType}>
              <SelectTrigger id="service-type">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="ClusterIP">ClusterIP</SelectItem>
                <SelectItem value="NodePort" disabled={view?.spec.headless === true}>
                  NodePort
                </SelectItem>
                <SelectItem value="LoadBalancer" disabled={view?.spec.headless === true}>
                  LoadBalancer
                </SelectItem>
                <SelectItem value="ExternalName">ExternalName</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          {externalNameService ? (
            <Field label="ExternalName" htmlFor="service-external-name">
              <Input
                id="service-external-name"
                value={externalName}
                autoComplete="off"
                spellCheck={false}
                placeholder="例如 example.com"
                onChange={(event) => setExternalName(event.target.value)}
              />
            </Field>
          ) : (
            <Field label="会话保持" htmlFor="service-affinity">
              <Select value={sessionAffinity} onValueChange={setSessionAffinity}>
                <SelectTrigger id="service-affinity">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={DEFAULT_OPTION}>默认（None）</SelectItem>
                  <SelectItem value="None">None</SelectItem>
                  <SelectItem value="ClientIP">ClientIP</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          )}
          {type === "NodePort" || type === "LoadBalancer" ? (
            <Field label="外部流量策略" htmlFor="service-external-policy">
              <Select value={externalPolicy} onValueChange={setExternalPolicy}>
                <SelectTrigger id="service-external-policy">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={DEFAULT_OPTION}>默认（Cluster）</SelectItem>
                  <SelectItem value="Cluster">Cluster</SelectItem>
                  <SelectItem value="Local">Local</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          ) : null}
        </div>
        {type === "ClusterIP" ? (
          <label className="mt-3 flex items-center gap-2 text-[13px]">
            <Checkbox
              checked={headless}
              disabled={headlessLocked}
              onCheckedChange={(checked) => setHeadless(checked === true)}
            />
            Headless（不分配 ClusterIP）{headlessLocked ? "；创建后不可切换" : ""}
          </label>
        ) : null}
      </FormSection>

      {externalNameService ? null : (
        <FormSection title="端口" hint="至少一个；目标端口留空表示与端口相同">
          <RowList
            rows={ports}
            onChange={setPorts}
            addLabel="添加端口"
            create={() => ({
              name: "",
              port: "",
              targetPort: "",
              protocol: DEFAULT_OPTION,
              nodePort: "",
            })}
            render={(row, index, update) => (
              <div className="grid grid-cols-[1fr_5rem_6rem_7rem_6rem] gap-2">
                <Input
                  value={row.name}
                  aria-label={`端口 ${index + 1} 名称`}
                  placeholder="名称"
                  autoComplete="off"
                  onChange={(event) => update({ name: event.target.value })}
                />
                <Input
                  value={row.port}
                  aria-label={`端口 ${index + 1} 端口`}
                  placeholder="端口"
                  inputMode="numeric"
                  onChange={(event) => update({ port: event.target.value })}
                />
                <Input
                  value={row.targetPort}
                  aria-label={`端口 ${index + 1} 目标端口`}
                  placeholder="目标端口"
                  autoComplete="off"
                  onChange={(event) => update({ targetPort: event.target.value })}
                />
                <Select value={row.protocol} onValueChange={(value) => update({ protocol: value })}>
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
                <Input
                  value={row.nodePort}
                  aria-label={`端口 ${index + 1} NodePort`}
                  placeholder="NodePort"
                  inputMode="numeric"
                  disabled={type === "ClusterIP"}
                  onChange={(event) => update({ nodePort: event.target.value })}
                />
              </div>
            )}
          />
        </FormSection>
      )}

      {externalNameService ? null : (
        <FormSection title="选择器" hint="留空表示不按标签选择 Pod，需要自行管理 Endpoints">
          <PairList rows={selector} onChange={setSelector} addLabel="添加选择器" />
        </FormSection>
      )}
    </>
  );

  return { fields, valid, build };
}

function useIngressDraft(existing: NetworkingSummary | null): SpecEditor {
  const view = existing?.ingress;
  const [className, setClassName] = useState(view?.spec.ingress_class_name ?? "");
  const currentDefaultBackend = view?.spec.default_backend;
  const [defaultBackend, setDefaultBackend] = useState<DefaultBackendDraft>({
    enabled: currentDefaultBackend !== undefined,
    name: currentDefaultBackend?.name ?? "",
    port: currentDefaultBackend?.port_name || String(currentDefaultBackend?.port_number || ""),
  });
  const [rules, setRules] = useState<RuleDraft[]>(
    (view?.spec.rules ?? []).map((rule) => ({
      host: rule.host,
      paths: rule.paths.map((path) => ({
        path: path.path,
        pathType: path.path_type,
        backendName: path.backend.name,
        backendPort: path.backend.port_name || String(path.backend.port_number || ""),
      })),
    })),
  );
  const [tls, setTls] = useState<TlsDraft[]>(
    (view?.spec.tls ?? []).map((entry) => ({
      hosts: entry.hosts.join(", "),
      secretName: entry.secret_name,
    })),
  );

  const rulesValid = rules.every(
    (rule) =>
      validHostname(rule.host) &&
      rule.paths.length > 0 &&
      rule.paths.every(
        (path) =>
          (path.path.trim() === "" || path.path.trim().startsWith("/")) &&
          validBackendDraft(path.backendName, path.backendPort),
      ),
  );
  const valid =
    (className.trim() === "" ||
      (DNS_SUBDOMAIN.test(className.trim()) && className.trim().length <= 253)) &&
    (defaultBackend.enabled || rules.length > 0) &&
    (!defaultBackend.enabled || validBackendDraft(defaultBackend.name, defaultBackend.port)) &&
    rulesValid &&
    tls.every((entry) => {
      if (entry.secretName.trim() === "" && entry.hosts.trim() === "") return true;
      return (
        (entry.secretName.trim() === "" ||
          (DNS_SUBDOMAIN.test(entry.secretName.trim()) && entry.secretName.trim().length <= 253)) &&
        splitCommaList(entry.hosts).every(validHostname)
      );
    });

  const build = (): NetworkingSpecInput => ({
    ingress: {
      ...(className.trim() ? { ingress_class_name: className.trim() } : {}),
      ...(defaultBackend.enabled
        ? {
            default_backend: backendInput(defaultBackend.name.trim(), defaultBackend.port.trim()),
          }
        : {}),
      rules: rules.map((rule) => ({
        ...(rule.host.trim() ? { host: rule.host.trim() } : {}),
        paths: rule.paths.map((path) => ({
          ...(path.path.trim() ? { path: path.path.trim() } : {}),
          path_type: path.pathType as "Exact" | "Prefix" | "ImplementationSpecific",
          backend: backendInput(path.backendName.trim(), path.backendPort.trim()),
        })),
      })),
      tls: tls
        .filter((entry) => entry.secretName.trim() !== "" || entry.hosts.trim() !== "")
        .map((entry) => ({
          ...(entry.secretName.trim() ? { secret_name: entry.secretName.trim() } : {}),
          hosts: entry.hosts
            .split(",")
            .map((host) => host.trim())
            .filter(Boolean),
        })),
    },
  });

  const fields = (
    <>
      <FormSection title="Ingress">
        <div className="grid gap-3">
          <Label htmlFor="ingress-class">IngressClass</Label>
          <Input
            id="ingress-class"
            value={className}
            autoComplete="off"
            spellCheck={false}
            placeholder="留空使用集群默认 IngressClass"
            onChange={(event) => setClassName(event.target.value)}
          />
          <label className="flex items-center gap-2 text-[13px]">
            <Checkbox
              checked={defaultBackend.enabled}
              onCheckedChange={(checked) =>
                setDefaultBackend((current) => ({ ...current, enabled: checked === true }))
              }
            />
            配置默认后端
          </label>
          {defaultBackend.enabled ? (
            <div className="grid grid-cols-[1fr_10rem] gap-2">
              <Input
                value={defaultBackend.name}
                aria-label="默认后端 Service"
                placeholder="Service 名称"
                autoComplete="off"
                onChange={(event) =>
                  setDefaultBackend((current) => ({ ...current, name: event.target.value }))
                }
              />
              <Input
                value={defaultBackend.port}
                aria-label="默认后端端口"
                placeholder="端口或名称"
                autoComplete="off"
                onChange={(event) =>
                  setDefaultBackend((current) => ({ ...current, port: event.target.value }))
                }
              />
            </div>
          ) : null}
        </div>
      </FormSection>

      <FormSection title="规则" hint="默认后端和规则至少配置一项；主机留空表示任意主机">
        <RowList
          rows={rules}
          onChange={setRules}
          addLabel="添加规则"
          create={() => ({
            host: "",
            paths: [{ path: "/", pathType: "Prefix", backendName: "", backendPort: "" }],
          })}
          render={(rule, ruleIndex, updateRule) => (
            <div className="border-border/60 grid gap-2 rounded-md border p-2">
              <Input
                value={rule.host}
                aria-label={`规则 ${ruleIndex + 1} 主机`}
                placeholder="主机，例如 api.example.com"
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => updateRule({ host: event.target.value })}
              />
              <RowList
                rows={rule.paths}
                onChange={(paths) => updateRule({ paths })}
                addLabel="添加路径"
                minimum={1}
                create={() => ({
                  path: "/",
                  pathType: "Prefix",
                  backendName: "",
                  backendPort: "",
                })}
                render={(path, pathIndex, updatePath) => (
                  <div className="grid grid-cols-[1fr_9rem_1fr_7rem] gap-2">
                    <Input
                      value={path.path}
                      aria-label={`路径 ${pathIndex + 1}`}
                      placeholder="路径"
                      autoComplete="off"
                      onChange={(event) => updatePath({ path: event.target.value })}
                    />
                    <Select
                      value={path.pathType}
                      onValueChange={(value) => updatePath({ pathType: value })}
                    >
                      <SelectTrigger aria-label={`路径 ${pathIndex + 1} 匹配方式`}>
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
                      aria-label={`路径 ${pathIndex + 1} 后端 Service`}
                      placeholder="后端 Service"
                      autoComplete="off"
                      onChange={(event) => updatePath({ backendName: event.target.value })}
                    />
                    <Input
                      value={path.backendPort}
                      aria-label={`路径 ${pathIndex + 1} 后端端口`}
                      placeholder="端口或名称"
                      autoComplete="off"
                      onChange={(event) => updatePath({ backendPort: event.target.value })}
                    />
                  </div>
                )}
              />
            </div>
          )}
        />
      </FormSection>

      <FormSection title="TLS" hint="可选；多个主机用逗号分隔">
        <RowList
          rows={tls}
          onChange={setTls}
          addLabel="添加 TLS"
          create={() => ({ hosts: "", secretName: "" })}
          render={(row, index, update) => (
            <div className="grid grid-cols-[1fr_1fr] gap-2">
              <Input
                value={row.hosts}
                aria-label={`TLS ${index + 1} 主机`}
                placeholder="主机，逗号分隔"
                autoComplete="off"
                onChange={(event) => update({ hosts: event.target.value })}
              />
              <Input
                value={row.secretName}
                aria-label={`TLS ${index + 1} Secret`}
                placeholder="Secret 名称"
                autoComplete="off"
                onChange={(event) => update({ secretName: event.target.value })}
              />
            </div>
          )}
        />
      </FormSection>
    </>
  );

  return { fields, valid, build };
}

/** A backend port is either a number or a Pod port name; the API takes them apart. */
function backendInput(name: string, port: string) {
  return /^\d+$/.test(port) ? { name, port_number: Number(port) } : { name, port_name: port };
}

function validPortNumber(value: string): boolean {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return false;
  const number = Number(trimmed);
  return number >= 1 && number <= 65535;
}

function validNamedPort(value: string): boolean {
  const trimmed = value.trim();
  return trimmed.length <= 15 && PORT_NAME.test(trimmed) && /[a-z]/.test(trimmed);
}

function validBackendDraft(name: string, port: string): boolean {
  const trimmedName = name.trim();
  const trimmedPort = port.trim();
  return (
    DNS1035_LABEL.test(trimmedName) &&
    trimmedName.length <= 63 &&
    (validPortNumber(trimmedPort) || validNamedPort(trimmedPort))
  );
}

function splitCommaList(value: string): string[] {
  return value
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

function validHostname(value: string): boolean {
  const hostname = value.trim();
  return (
    hostname === "" ||
    (DNS_SUBDOMAIN.test(hostname) && hostname.length <= 253) ||
    (hostname.startsWith("*.") && DNS_SUBDOMAIN.test(hostname.slice(2)) && hostname.length <= 253)
  );
}

function validGatewayListener(listener: ListenerDraft): boolean {
  const name = listener.name.trim();
  const certificates = splitCommaList(listener.certificateSecrets);
  const certificatesValid = certificates.every(
    (certificate) => DNS_SUBDOMAIN.test(certificate) && certificate.length <= 253,
  );
  if (
    name === "" ||
    name.length > 63 ||
    !PORT_NAME.test(name) ||
    !validPortNumber(listener.port) ||
    !validHostname(listener.hostname) ||
    !certificatesValid
  ) {
    return false;
  }
  if (listener.protocol === "HTTPS") {
    return listener.tlsMode === "Terminate" && certificates.length > 0;
  }
  if (listener.protocol === "TLS") {
    return (
      listener.tlsMode === "Passthrough" ||
      (listener.tlsMode === "Terminate" && certificates.length > 0)
    );
  }
  return true;
}

function useGatewayDraft(existing: NetworkingSummary | null): SpecEditor {
  const view = existing?.gateway;
  const [className, setClassName] = useState(view?.spec.gateway_class_name ?? "");
  const [listeners, setListeners] = useState<ListenerDraft[]>(
    (view?.spec.listeners ?? []).map((listener) => ({
      originalName: listener.name,
      name: listener.name,
      hostname: listener.hostname,
      port: String(listener.port),
      protocol: listener.protocol,
      tlsMode: listener.tls?.mode ?? DEFAULT_OPTION,
      certificateSecrets:
        listener.tls?.certificate_refs.map((reference) => reference.name).join(", ") ?? "",
      namespacesFrom: listener.allowed_routes.namespaces_from || DEFAULT_OPTION,
    })),
  );

  const listenerNames = listeners.map((listener) => listener.name.trim()).filter(Boolean);
  const valid =
    DNS_SUBDOMAIN.test(className.trim()) &&
    className.trim().length <= 253 &&
    listeners.length > 0 &&
    listeners.every(validGatewayListener) &&
    new Set(listenerNames).size === listenerNames.length;

  const build = (): NetworkingSpecInput => {
    const gateway: KubernetesGatewaySpecInput = {
      gateway_class_name: className.trim(),
      ...(view?.spec.addresses.length
        ? {
            addresses: view.spec.addresses.map((address) => ({
              ...(address.type ? { type: address.type } : {}),
              value: address.value,
            })),
          }
        : {}),
      listeners: listeners.map((listener) => {
        const original = view?.spec.listeners.find(
          (candidate) => candidate.name === listener.originalName,
        );
        const protocol = listener.protocol as "HTTP" | "HTTPS" | "TLS" | "TCP" | "UDP";
        const tlsCapable = protocol === "HTTPS" || protocol === "TLS";
        const certificateNames = splitCommaList(listener.certificateSecrets);
        const originalCertificateNames =
          original?.tls?.certificate_refs.map((reference) => reference.name).join(", ") ?? "";
        const preserveCertificateReferences =
          listener.certificateSecrets.trim() === originalCertificateNames &&
          listener.tlsMode === original?.tls?.mode;
        const certificateRefs = preserveCertificateReferences
          ? original?.tls?.certificate_refs.map((reference) => ({
              ...(reference.group ? { group: reference.group } : {}),
              ...(reference.kind ? { kind: reference.kind } : {}),
              name: reference.name,
              ...(reference.namespace ? { namespace: reference.namespace } : {}),
            }))
          : certificateNames.map((name) => ({ name }));
        const routeKinds =
          original?.allowed_routes.kinds.map((kind) => ({
            ...(kind.group ? { group: kind.group } : {}),
            kind: kind.kind,
          })) ?? [];
        const includeAllowedRoutes =
          listener.namespacesFrom !== DEFAULT_OPTION || routeKinds.length > 0;

        return {
          name: listener.name.trim(),
          ...(listener.hostname.trim() ? { hostname: listener.hostname.trim() } : {}),
          port: Number(listener.port.trim()),
          protocol,
          ...(tlsCapable && listener.tlsMode !== DEFAULT_OPTION
            ? {
                tls: {
                  mode: listener.tlsMode as "Terminate" | "Passthrough",
                  ...(listener.tlsMode === "Terminate"
                    ? { certificate_refs: certificateRefs }
                    : {}),
                },
              }
            : {}),
          ...(includeAllowedRoutes
            ? {
                allowed_routes: {
                  ...(listener.namespacesFrom !== DEFAULT_OPTION
                    ? {
                        namespaces_from: listener.namespacesFrom as "Same" | "All" | "Selector",
                      }
                    : {}),
                  ...(listener.namespacesFrom === "Selector" && original?.allowed_routes.selector
                    ? { selector: original.allowed_routes.selector }
                    : {}),
                  ...(routeKinds.length > 0 ? { kinds: routeKinds } : {}),
                },
              }
            : {}),
        };
      }),
    };
    return { gateway };
  };

  const fields = (
    <>
      <FormSection title="Gateway">
        <div className="grid gap-1.5">
          <Label htmlFor="gateway-class">GatewayClass</Label>
          <Input
            id="gateway-class"
            value={className}
            autoComplete="off"
            spellCheck={false}
            placeholder="例如 istio"
            onChange={(event) => setClassName(event.target.value)}
          />
        </div>
      </FormSection>

      <FormSection title="监听器" hint="至少一个；TLS 证书取同一命名空间中的 Secret">
        <RowList
          rows={listeners}
          onChange={setListeners}
          addLabel="添加监听器"
          create={() => ({
            originalName: null,
            name: "",
            hostname: "",
            port: "80",
            protocol: "HTTP",
            tlsMode: DEFAULT_OPTION,
            certificateSecrets: "",
            namespacesFrom: DEFAULT_OPTION,
          })}
          render={(listener, index, update) => (
            <div className="border-border/60 grid gap-2 rounded-md border p-2">
              <div className="grid grid-cols-[1fr_1fr_5rem_7rem] gap-2">
                <Input
                  value={listener.name}
                  aria-label={`监听器 ${index + 1} 名称`}
                  placeholder="名称"
                  autoComplete="off"
                  onChange={(event) => update({ name: event.target.value })}
                />
                <Input
                  value={listener.hostname}
                  aria-label={`监听器 ${index + 1} 主机`}
                  placeholder="主机，可留空"
                  autoComplete="off"
                  onChange={(event) => update({ hostname: event.target.value })}
                />
                <Input
                  value={listener.port}
                  aria-label={`监听器 ${index + 1} 端口`}
                  placeholder="端口"
                  inputMode="numeric"
                  onChange={(event) => update({ port: event.target.value })}
                />
                <Select
                  value={listener.protocol}
                  onValueChange={(value) =>
                    update({
                      protocol: value,
                      ...(value === "HTTPS" || value === "TLS"
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
                  onValueChange={(value) => update({ tlsMode: value })}
                  disabled={listener.protocol !== "HTTPS" && listener.protocol !== "TLS"}
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
                  disabled={listener.tlsMode !== "Terminate"}
                  onChange={(event) => update({ certificateSecrets: event.target.value })}
                />
                <Select
                  value={listener.namespacesFrom}
                  onValueChange={(value) => update({ namespacesFrom: value })}
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
          )}
        />
      </FormSection>
    </>
  );

  return { fields, valid, build };
}

function FormSection({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <section>
      <div className="mb-2 flex items-baseline gap-2">
        <h4 className="text-foreground text-[13px] font-medium">{title}</h4>
        {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
      </div>
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
