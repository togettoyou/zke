import { Plus, X } from "lucide-react";
import type { ReactNode } from "react";

import type { KubernetesNetworkingResource } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input, NumericInput } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

import {
  emptyGatewayRouteRule,
  emptyGRPCRouteMatch,
  emptyHTTPRouteMatch,
  emptyRouteBackend,
  emptyRouteParent,
  emptyRouteValueMatch,
  type GatewayRouteDraft,
  type GatewayRouteRuleDraft,
  type GRPCRouteMatchDraft,
  type HTTPRouteMatchDraft,
  type RouteBackendDraft,
  type RouteParentDraft,
  type RouteValueMatchDraft,
} from "./gateway-route-form-model";
import { DEFAULT_OPTION, PORT_DIGITS } from "./networking-form-model";

export function GatewayRouteEditor({
  resource,
  draft,
  onChange,
}: {
  resource: KubernetesNetworkingResource;
  draft: GatewayRouteDraft;
  onChange: (draft: GatewayRouteDraft) => void;
}) {
  const patch = (changes: Partial<GatewayRouteDraft>) => onChange({ ...draft, ...changes });
  const supportsHostnames =
    resource === "httproutes" || resource === "grpcroutes" || resource === "tlsroutes";

  return (
    <div className="grid gap-3">
      <EditorGroup
        title="关联 Gateway"
        hint="可配置多个父级；跨命名空间引用仍需目标 Gateway 的 AllowedRoutes 或相应授权"
      >
        <RowList
          rows={draft.parents}
          onChange={(parents) => patch({ parents })}
          create={emptyRouteParent}
          addLabel="添加父级"
          render={(parent, index, update) => (
            <ParentRow parent={parent} index={index} update={update} />
          )}
        />
      </EditorGroup>

      {supportsHostnames ? (
        <EditorGroup
          title={resource === "tlsroutes" ? "SNI 主机名" : "主机名"}
          hint={
            resource === "tlsroutes"
              ? "必填；多个主机名用逗号分隔，可使用 *.example.com"
              : "可选；留空匹配父级监听器允许的任意主机，多个主机名用逗号分隔"
          }
        >
          <Input
            value={draft.hostnames}
            aria-label="Route 主机名"
            placeholder="例如 api.example.com, *.internal.example.com"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => patch({ hostnames: event.target.value })}
          />
        </EditorGroup>
      ) : null}

      <EditorGroup
        title="路由规则"
        hint="同一规则中的多个匹配条件为 OR；一个匹配条件中的各字段为 AND"
      >
        <RowList
          rows={draft.rules}
          onChange={(rules) => patch({ rules })}
          create={() => emptyGatewayRouteRule(resource)}
          addLabel="添加规则"
          minimum={1}
          roomy
          render={(rule, index, update) => (
            <RouteRule resource={resource} rule={rule} index={index} update={update} />
          )}
        />
      </EditorGroup>

      <p className="text-subtle-foreground text-xs leading-5">
        当前表单覆盖父级、主机名、HTTP/gRPC
        匹配及后端引用。已有对象中的过滤器、超时、重试、会话保持与 Controller
        扩展会原样保留；如需修改这些高级字段，请使用详情页的 YAML 编辑器。DryRun
        仍会执行目标集群实际 CRD/CEL 与准入校验。
      </p>
    </div>
  );
}

function ParentRow({
  parent,
  index,
  update,
}: {
  parent: RouteParentDraft;
  index: number;
  update: (patch: Partial<RouteParentDraft>) => void;
}) {
  return (
    <div className="border-border/60 grid gap-2 rounded-md border p-2">
      <div className="grid gap-2 md:grid-cols-3">
        <ReferenceInput
          value={parent.group}
          defaultValue="gateway.networking.k8s.io"
          coreAlias
          ariaLabel={`父级 ${index + 1} API Group`}
          placeholder="API Group（默认 Gateway API）"
          onChange={(group) => update({ group })}
        />
        <ReferenceInput
          value={parent.kind}
          defaultValue="Gateway"
          ariaLabel={`父级 ${index + 1} Kind`}
          placeholder="Kind（默认 Gateway）"
          onChange={(kind) => update({ kind })}
        />
        <Input
          value={parent.name}
          aria-label={`父级 ${index + 1} 名称`}
          placeholder="Gateway 名称"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ name: event.target.value })}
        />
      </div>
      <div className="grid gap-2 md:grid-cols-3">
        <Input
          value={parent.namespace}
          aria-label={`父级 ${index + 1} 命名空间`}
          placeholder="命名空间（默认当前）"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ namespace: event.target.value })}
        />
        <Input
          value={parent.sectionName}
          aria-label={`父级 ${index + 1} 监听器`}
          placeholder="监听器/Section（可选）"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ sectionName: event.target.value })}
        />
        <NumericInput
          value={parent.port}
          aria-label={`父级 ${index + 1} 端口`}
          placeholder="监听端口（可选）"
          maxLength={PORT_DIGITS}
          onValueChange={(port) => update({ port })}
        />
      </div>
    </div>
  );
}

function RouteRule({
  resource,
  rule,
  index,
  update,
}: {
  resource: KubernetesNetworkingResource;
  rule: GatewayRouteRuleDraft;
  index: number;
  update: (patch: Partial<GatewayRouteRuleDraft>) => void;
}) {
  return (
    <div className="border-border/70 bg-surface-muted/30 grid gap-3 rounded-md border p-3">
      <div className="grid gap-1.5">
        <Label>规则 {index + 1}</Label>
        <Input
          value={rule.name}
          aria-label={`规则 ${index + 1} 名称`}
          placeholder="规则名称（可选）"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ name: event.target.value })}
        />
      </div>

      {resource === "httproutes" ? (
        <MatchSection
          title="HTTP 匹配"
          hint="不添加时匹配全部 HTTP 请求"
          rows={rule.httpMatches}
          onChange={(httpMatches) => update({ httpMatches })}
          create={emptyHTTPRouteMatch}
          render={(match, matchIndex, updateMatch) => (
            <HTTPMatchRow
              match={match}
              ruleIndex={index}
              matchIndex={matchIndex}
              update={updateMatch}
            />
          )}
        />
      ) : null}

      {resource === "grpcroutes" ? (
        <MatchSection
          title="gRPC 匹配"
          hint="不添加时匹配全部 gRPC 请求"
          rows={rule.grpcMatches}
          onChange={(grpcMatches) => update({ grpcMatches })}
          create={emptyGRPCRouteMatch}
          render={(match, matchIndex, updateMatch) => (
            <GRPCMatchRow
              match={match}
              ruleIndex={index}
              matchIndex={matchIndex}
              update={updateMatch}
            />
          )}
        />
      ) : null}

      <MatchSection
        title="后端目标"
        hint="默认引用当前命名空间的 core/v1 Service；权重留空等同于 1"
        rows={rule.backends}
        onChange={(backends) => update({ backends })}
        create={emptyRouteBackend}
        render={(backend, backendIndex, updateBackend) => (
          <BackendRow
            backend={backend}
            ruleIndex={index}
            backendIndex={backendIndex}
            update={updateBackend}
          />
        )}
      />
    </div>
  );
}

function HTTPMatchRow({
  match,
  ruleIndex,
  matchIndex,
  update,
}: {
  match: HTTPRouteMatchDraft;
  ruleIndex: number;
  matchIndex: number;
  update: (patch: Partial<HTTPRouteMatchDraft>) => void;
}) {
  const prefix = `规则 ${ruleIndex + 1} HTTP 匹配 ${matchIndex + 1}`;
  return (
    <div className="border-border/60 grid gap-2 rounded-md border p-2">
      <div className="grid gap-2 md:grid-cols-[10rem_1fr_10rem]">
        <Select value={match.pathType} onValueChange={(pathType) => update({ pathType })}>
          <SelectTrigger aria-label={`${prefix} 路径类型`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={DEFAULT_OPTION}>任意路径</SelectItem>
            <SelectItem value="PathPrefix">前缀</SelectItem>
            <SelectItem value="Exact">精确</SelectItem>
            <SelectItem value="RegularExpression">正则（实现相关）</SelectItem>
          </SelectContent>
        </Select>
        <Input
          value={match.pathValue}
          aria-label={`${prefix} 路径`}
          placeholder="例如 /api"
          disabled={match.pathType === DEFAULT_OPTION}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ pathValue: event.target.value })}
        />
        <Select value={match.method} onValueChange={(method) => update({ method })}>
          <SelectTrigger aria-label={`${prefix} HTTP 方法`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={DEFAULT_OPTION}>任意方法</SelectItem>
            {["GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH"].map(
              (method) => (
                <SelectItem key={method} value={method}>
                  {method}
                </SelectItem>
              ),
            )}
          </SelectContent>
        </Select>
      </div>
      <ValueMatchRows
        label="Header"
        prefix={`${prefix} Header`}
        rows={match.headers}
        onChange={(headers) => update({ headers })}
      />
      <ValueMatchRows
        label="查询参数"
        prefix={`${prefix} 查询参数`}
        rows={match.queryParams}
        onChange={(queryParams) => update({ queryParams })}
      />
    </div>
  );
}

function GRPCMatchRow({
  match,
  ruleIndex,
  matchIndex,
  update,
}: {
  match: GRPCRouteMatchDraft;
  ruleIndex: number;
  matchIndex: number;
  update: (patch: Partial<GRPCRouteMatchDraft>) => void;
}) {
  const prefix = `规则 ${ruleIndex + 1} gRPC 匹配 ${matchIndex + 1}`;
  return (
    <div className="border-border/60 grid gap-2 rounded-md border p-2">
      <div className="grid gap-2 md:grid-cols-[12rem_1fr_1fr]">
        <Select value={match.methodType} onValueChange={(methodType) => update({ methodType })}>
          <SelectTrigger aria-label={`${prefix} 方法类型`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={DEFAULT_OPTION}>任意方法</SelectItem>
            <SelectItem value="Exact">精确</SelectItem>
            <SelectItem value="RegularExpression">正则（实现相关）</SelectItem>
          </SelectContent>
        </Select>
        <Input
          value={match.service}
          aria-label={`${prefix} Service`}
          placeholder="Service，例如 io.zke.API"
          disabled={match.methodType === DEFAULT_OPTION}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ service: event.target.value })}
        />
        <Input
          value={match.method}
          aria-label={`${prefix} Method`}
          placeholder="Method，例如 GetCluster"
          disabled={match.methodType === DEFAULT_OPTION}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ method: event.target.value })}
        />
      </div>
      <ValueMatchRows
        label="Header"
        prefix={`${prefix} Header`}
        rows={match.headers}
        onChange={(headers) => update({ headers })}
      />
    </div>
  );
}

function BackendRow({
  backend,
  ruleIndex,
  backendIndex,
  update,
}: {
  backend: RouteBackendDraft;
  ruleIndex: number;
  backendIndex: number;
  update: (patch: Partial<RouteBackendDraft>) => void;
}) {
  const prefix = `规则 ${ruleIndex + 1} 后端 ${backendIndex + 1}`;
  return (
    <div className="border-border/60 grid gap-2 rounded-md border p-2">
      <div className="grid gap-2 md:grid-cols-3">
        <ReferenceInput
          value={backend.group}
          defaultValue="core/v1"
          ariaLabel={`${prefix} API Group`}
          placeholder="API Group（默认 core）"
          onChange={(group) => update({ group })}
        />
        <ReferenceInput
          value={backend.kind}
          defaultValue="Service"
          ariaLabel={`${prefix} Kind`}
          placeholder="Kind（默认 Service）"
          onChange={(kind) => update({ kind })}
        />
        <Input
          value={backend.name}
          aria-label={`${prefix} 名称`}
          placeholder="资源名称"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ name: event.target.value })}
        />
      </div>
      <div className="grid gap-2 md:grid-cols-3">
        <Input
          value={backend.namespace}
          aria-label={`${prefix} 命名空间`}
          placeholder="命名空间（默认当前）"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => update({ namespace: event.target.value })}
        />
        <NumericInput
          value={backend.port}
          aria-label={`${prefix} 端口`}
          placeholder="Service 端口"
          maxLength={PORT_DIGITS}
          onValueChange={(port) => update({ port })}
        />
        <NumericInput
          value={backend.weight}
          aria-label={`${prefix} 权重`}
          placeholder="权重（默认 1）"
          maxLength={7}
          onValueChange={(weight) => update({ weight })}
        />
      </div>
    </div>
  );
}

function ValueMatchRows({
  label,
  prefix,
  rows,
  onChange,
}: {
  label: string;
  prefix: string;
  rows: RouteValueMatchDraft[];
  onChange: (rows: RouteValueMatchDraft[]) => void;
}) {
  return (
    <div className="grid gap-1.5">
      <Label>{label}</Label>
      <RowList
        rows={rows}
        onChange={onChange}
        create={emptyRouteValueMatch}
        addLabel={`添加${label}`}
        render={(row, index, update) => (
          <div className="grid gap-2 md:grid-cols-[10rem_1fr_1fr]">
            <Select value={row.type} onValueChange={(type) => update({ type })}>
              <SelectTrigger aria-label={`${prefix} ${index + 1} 类型`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="Exact">精确</SelectItem>
                <SelectItem value="RegularExpression">正则（实现相关）</SelectItem>
              </SelectContent>
            </Select>
            <Input
              value={row.name}
              aria-label={`${prefix} ${index + 1} 名称`}
              placeholder="名称"
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => update({ name: event.target.value })}
            />
            <Input
              value={row.value}
              aria-label={`${prefix} ${index + 1} 值`}
              placeholder="匹配值"
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => update({ value: event.target.value })}
            />
          </div>
        )}
      />
    </div>
  );
}

function MatchSection<T>({
  title,
  hint,
  rows,
  onChange,
  create,
  render,
}: {
  title: string;
  hint: string;
  rows: T[];
  onChange: (rows: T[]) => void;
  create: () => T;
  render: (row: T, index: number, update: (patch: Partial<T>) => void) => ReactNode;
}) {
  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap items-baseline gap-2">
        <Label>{title}</Label>
        <span className="text-subtle-foreground text-xs">{hint}</span>
      </div>
      <RowList
        rows={rows}
        onChange={onChange}
        create={create}
        addLabel={`添加${title}`}
        render={render}
      />
    </div>
  );
}

function ReferenceInput({
  value,
  defaultValue,
  ariaLabel,
  placeholder,
  onChange,
  coreAlias = false,
}: {
  value: string;
  defaultValue: string;
  ariaLabel: string;
  placeholder: string;
  onChange: (value: string) => void;
  coreAlias?: boolean;
}) {
  const displayed = value === DEFAULT_OPTION ? "" : coreAlias && value === "" ? "core/v1" : value;
  return (
    <Input
      value={displayed}
      aria-label={ariaLabel}
      placeholder={placeholder}
      title={`留空使用 ${defaultValue}`}
      autoComplete="off"
      spellCheck={false}
      onChange={(event) => {
        const next = event.target.value;
        onChange(next === "" ? DEFAULT_OPTION : coreAlias && next === "core/v1" ? "" : next);
      }}
    />
  );
}

function EditorGroup({
  title,
  hint,
  children,
}: {
  title: string;
  hint: string;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap items-baseline gap-2">
        <Label>{title}</Label>
        <span className="text-subtle-foreground text-xs">{hint}</span>
      </div>
      {children}
    </div>
  );
}

function RowList<T>({
  rows,
  onChange,
  addLabel,
  create,
  render,
  minimum = 0,
  roomy = false,
}: {
  rows: T[];
  onChange: (rows: T[]) => void;
  addLabel: string;
  create: () => T;
  render: (row: T, index: number, update: (patch: Partial<T>) => void) => ReactNode;
  minimum?: number;
  roomy?: boolean;
}) {
  return (
    <div className={roomy ? "grid gap-3" : "grid gap-2"}>
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-2">
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
