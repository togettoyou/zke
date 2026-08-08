import { Plus, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input, NumericInput } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

import {
  DEFAULT_OPTION,
  type KeyValueDraft,
  type WorkloadAffinityDraft,
  type WorkloadLabelSelectorDraft,
  type WorkloadNodeAffinityDraft,
  type WorkloadNodeSelectorRequirementDraft,
  type WorkloadNodeSelectorTermDraft,
  type WorkloadPodAffinityDraft,
  type WorkloadPodAffinityTermDraft,
  type WorkloadSelectorRequirementDraft,
  type WorkloadTopologySpreadConstraintDraft,
} from "./workload-form-model";

type AffinityPart = "node_affinity" | "pod_affinity" | "pod_anti_affinity";

export function AdvancedSchedulingEditor({
  affinity,
  topologySpreadConstraints,
  onAffinityChange,
  onTopologySpreadChange,
}: {
  affinity: WorkloadAffinityDraft;
  topologySpreadConstraints: WorkloadTopologySpreadConstraintDraft[];
  onAffinityChange: (affinity: WorkloadAffinityDraft) => void;
  onTopologySpreadChange: (constraints: WorkloadTopologySpreadConstraintDraft[]) => void;
}) {
  const ruleCount = (value?: { required?: unknown[]; preferred?: unknown[] }) =>
    (value?.required?.length ?? 0) + (value?.preferred?.length ?? 0);
  const updatePart = (
    key: AffinityPart,
    value: WorkloadNodeAffinityDraft | WorkloadPodAffinityDraft,
  ) => {
    const next = { ...affinity };
    const empty = (value.required?.length ?? 0) === 0 && (value.preferred?.length ?? 0) === 0;
    if (empty) {
      delete next[key];
    } else if (key === "node_affinity") {
      next.node_affinity = value as WorkloadNodeAffinityDraft;
    } else if (key === "pod_affinity") {
      next.pod_affinity = value as WorkloadPodAffinityDraft;
    } else {
      next.pod_anti_affinity = value as WorkloadPodAffinityDraft;
    }
    onAffinityChange(next);
  };

  return (
    <Tabs defaultValue="node">
      <TabsList className="h-auto max-w-full flex-wrap">
        <TabsTrigger value="node">
          节点亲和性
          <RuleCount value={ruleCount(affinity.node_affinity)} />
        </TabsTrigger>
        <TabsTrigger value="pod">
          Pod 亲和性
          <RuleCount value={ruleCount(affinity.pod_affinity)} />
        </TabsTrigger>
        <TabsTrigger value="anti">
          Pod 反亲和性
          <RuleCount value={ruleCount(affinity.pod_anti_affinity)} />
        </TabsTrigger>
        <TabsTrigger value="topology">
          拓扑分布
          <RuleCount value={topologySpreadConstraints.length} />
        </TabsTrigger>
      </TabsList>

      <TabsContent value="node">
        <NodeAffinityEditor
          value={affinity.node_affinity ?? {}}
          onChange={(value) => updatePart("node_affinity", value)}
        />
      </TabsContent>
      <TabsContent value="pod">
        <PodAffinityEditor
          title="Pod 亲和性"
          value={affinity.pod_affinity ?? {}}
          onChange={(value) => updatePart("pod_affinity", value)}
        />
      </TabsContent>
      <TabsContent value="anti">
        <PodAffinityEditor
          title="Pod 反亲和性"
          value={affinity.pod_anti_affinity ?? {}}
          onChange={(value) => updatePart("pod_anti_affinity", value)}
        />
      </TabsContent>
      <TabsContent value="topology">
        <TopologySpreadEditor
          values={topologySpreadConstraints}
          onChange={onTopologySpreadChange}
        />
      </TabsContent>
    </Tabs>
  );
}

function NodeAffinityEditor({
  value,
  onChange,
}: {
  value: WorkloadNodeAffinityDraft;
  onChange: (value: WorkloadNodeAffinityDraft) => void;
}) {
  const required = value.required ?? [];
  const preferred = value.preferred ?? [];
  return (
    <div className="grid gap-4">
      <RuleGroupHeader
        title="必须满足"
        hint="规则组之间是 OR；每组里的标签和字段条件是 AND。"
        addLabel="添加硬性规则组"
        onAdd={() =>
          onChange({
            ...value,
            required: [...required, emptyNodeTerm()],
          })
        }
      />
      {required.map((term, index) => (
        <NodeTermCard
          key={index}
          title={`硬性规则组 ${index + 1}`}
          term={term}
          onChange={(term) =>
            onChange({
              ...value,
              required: required.map((item, position) => (position === index ? term : item)),
            })
          }
          onRemove={() =>
            onChange({
              ...value,
              required: required.filter((_, position) => position !== index),
            })
          }
        />
      ))}
      {required.length === 0 ? <EmptyHint>未配置必须满足的节点规则。</EmptyHint> : null}

      <RuleGroupHeader
        title="优先满足"
        hint="不满足时仍可调度；权重越高，调度器越偏好该规则组。"
        addLabel="添加偏好规则组"
        onAdd={() =>
          onChange({
            ...value,
            preferred: [...preferred, { weight: 1, preference: emptyNodeTerm() }],
          })
        }
      />
      {preferred.map((weighted, index) => (
        <NodeTermCard
          key={index}
          title={`偏好规则组 ${index + 1}`}
          term={weighted.preference}
          weight={weighted.weight}
          onWeightChange={(weight) =>
            onChange({
              ...value,
              preferred: preferred.map((item, position) =>
                position === index ? { ...item, weight } : item,
              ),
            })
          }
          onChange={(preference) =>
            onChange({
              ...value,
              preferred: preferred.map((item, position) =>
                position === index ? { ...item, preference } : item,
              ),
            })
          }
          onRemove={() =>
            onChange({
              ...value,
              preferred: preferred.filter((_, position) => position !== index),
            })
          }
        />
      ))}
      {preferred.length === 0 ? <EmptyHint>未配置优先满足的节点规则。</EmptyHint> : null}
    </div>
  );
}

function NodeTermCard({
  title,
  term,
  weight,
  onWeightChange,
  onChange,
  onRemove,
}: {
  title: string;
  term: WorkloadNodeSelectorTermDraft;
  weight?: number;
  onWeightChange?: (weight: number) => void;
  onChange: (term: WorkloadNodeSelectorTermDraft) => void;
  onRemove: () => void;
}) {
  return (
    <RuleCard title={title} onRemove={onRemove}>
      {onWeightChange ? (
        <Labeled label="权重" hint="1–100">
          <NumericInput
            value={weight ? String(weight) : ""}
            className="max-w-36"
            onValueChange={(value) => onWeightChange(Number(value))}
          />
        </Labeled>
      ) : null}
      <RequirementRows
        label="节点标签条件"
        hint="同一组内全部条件都必须成立。"
        rows={term.match_expressions ?? []}
        node
        onChange={(match_expressions) => onChange({ ...term, match_expressions })}
      />
      <RequirementRows
        label="节点字段条件"
        hint="通常使用 metadata.name；与上面的标签条件也是 AND。"
        rows={term.match_fields ?? []}
        node
        onChange={(match_fields) => onChange({ ...term, match_fields })}
      />
    </RuleCard>
  );
}

function PodAffinityEditor({
  title,
  value,
  onChange,
}: {
  title: string;
  value: WorkloadPodAffinityDraft;
  onChange: (value: WorkloadPodAffinityDraft) => void;
}) {
  const required = value.required ?? [];
  const preferred = value.preferred ?? [];
  return (
    <div className="grid gap-4">
      <RuleGroupHeader
        title={`${title} · 必须满足`}
        hint="每条都是独立硬性约束，全部都必须满足。"
        addLabel="添加硬性规则"
        onAdd={() => onChange({ ...value, required: [...required, emptyPodTerm()] })}
      />
      {required.map((term, index) => (
        <PodTermCard
          key={index}
          title={`硬性规则 ${index + 1}`}
          term={term}
          onChange={(term) =>
            onChange({
              ...value,
              required: required.map((item, position) => (position === index ? term : item)),
            })
          }
          onRemove={() =>
            onChange({
              ...value,
              required: required.filter((_, position) => position !== index),
            })
          }
        />
      ))}
      {required.length === 0 ? <EmptyHint>未配置硬性规则。</EmptyHint> : null}

      <RuleGroupHeader
        title={`${title} · 优先满足`}
        hint="不满足时仍可调度，按权重参与节点打分。"
        addLabel="添加偏好规则"
        onAdd={() =>
          onChange({
            ...value,
            preferred: [...preferred, { weight: 1, pod_term: emptyPodTerm() }],
          })
        }
      />
      {preferred.map((weighted, index) => (
        <PodTermCard
          key={index}
          title={`偏好规则 ${index + 1}`}
          term={weighted.pod_term}
          weight={weighted.weight}
          onWeightChange={(weight) =>
            onChange({
              ...value,
              preferred: preferred.map((item, position) =>
                position === index ? { ...item, weight } : item,
              ),
            })
          }
          onChange={(pod_term) =>
            onChange({
              ...value,
              preferred: preferred.map((item, position) =>
                position === index ? { ...item, pod_term } : item,
              ),
            })
          }
          onRemove={() =>
            onChange({
              ...value,
              preferred: preferred.filter((_, position) => position !== index),
            })
          }
        />
      ))}
      {preferred.length === 0 ? <EmptyHint>未配置偏好规则。</EmptyHint> : null}
    </div>
  );
}

function PodTermCard({
  title,
  term,
  weight,
  onWeightChange,
  onChange,
  onRemove,
}: {
  title: string;
  term: WorkloadPodAffinityTermDraft;
  weight?: number;
  onWeightChange?: (weight: number) => void;
  onChange: (term: WorkloadPodAffinityTermDraft) => void;
  onRemove: () => void;
}) {
  return (
    <RuleCard title={title} onRemove={onRemove}>
      <div className="grid gap-3 md:grid-cols-2">
        {onWeightChange ? (
          <Labeled label="权重" hint="1–100">
            <NumericInput
              value={weight ? String(weight) : ""}
              onValueChange={(value) => onWeightChange(Number(value))}
            />
          </Labeled>
        ) : null}
        <Labeled label="拓扑键" hint="例如 kubernetes.io/hostname 或 topology.kubernetes.io/zone">
          <Input
            value={term.topology_key}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => onChange({ ...term, topology_key: event.target.value })}
          />
        </Labeled>
      </div>

      <SelectorToggle
        label="Pod 标签选择器"
        hint="未启用表示不指定 Pod；启用后空选择器表示选择范围内的全部 Pod。"
        value={term.label_selector}
        onChange={(label_selector) => onChange({ ...term, label_selector })}
      />
      <SelectorToggle
        label="命名空间标签选择器"
        hint="启用后空选择器表示全部命名空间；可与显式命名空间列表组合。"
        value={term.namespace_selector}
        onChange={(namespace_selector) => onChange({ ...term, namespace_selector })}
      />

      <div className="grid gap-3 lg:grid-cols-3">
        <StringListEditor
          label="显式命名空间"
          values={term.namespaces ?? []}
          addLabel="添加命名空间"
          placeholder="例如 production"
          onChange={(namespaces) => onChange({ ...term, namespaces })}
        />
        <StringListEditor
          label="动态匹配标签键"
          hint="从新 Pod 的同名标签取值参与匹配；需要启用 Pod 标签选择器。"
          values={term.match_label_keys ?? []}
          addLabel="添加匹配键"
          placeholder="例如 pod-template-hash"
          onChange={(match_label_keys) => onChange({ ...term, match_label_keys })}
        />
        <StringListEditor
          label="动态排除标签键"
          hint="从新 Pod 的同名标签取值参与反向匹配。"
          values={term.mismatch_label_keys ?? []}
          addLabel="添加排除键"
          placeholder="例如 tenant"
          onChange={(mismatch_label_keys) => onChange({ ...term, mismatch_label_keys })}
        />
      </div>
    </RuleCard>
  );
}

function TopologySpreadEditor({
  values,
  onChange,
}: {
  values: WorkloadTopologySpreadConstraintDraft[];
  onChange: (values: WorkloadTopologySpreadConstraintDraft[]) => void;
}) {
  return (
    <div className="grid gap-4">
      <RuleGroupHeader
        title="拓扑分布约束"
        hint="让同一工作负载的 Pod 在节点、可用区或其他拓扑域之间均匀分布。"
        addLabel="添加分布约束"
        onAdd={() => onChange([...values, emptyTopologySpread()])}
      />
      {values.map((value, index) => (
        <RuleCard
          key={index}
          title={`分布约束 ${index + 1}`}
          onRemove={() => onChange(values.filter((_, position) => position !== index))}
        >
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            <Labeled label="拓扑键" hint="按该节点标签划分拓扑域">
              <Input
                value={value.topology_key}
                autoComplete="off"
                spellCheck={false}
                onChange={(event) =>
                  updateAt(values, index, { ...value, topology_key: event.target.value }, onChange)
                }
              />
            </Labeled>
            <Labeled label="最大偏差" hint="必须大于 0">
              <NumericInput
                value={value.max_skew ? String(value.max_skew) : ""}
                onValueChange={(maxSkew) =>
                  updateAt(values, index, { ...value, max_skew: Number(maxSkew) }, onChange)
                }
              />
            </Labeled>
            <Labeled label="无法满足时">
              <Select
                value={value.when_unsatisfiable}
                onValueChange={(when_unsatisfiable: "DoNotSchedule" | "ScheduleAnyway") =>
                  updateAt(
                    values,
                    index,
                    {
                      ...value,
                      when_unsatisfiable,
                      ...(when_unsatisfiable === "ScheduleAnyway"
                        ? { min_domains: undefined }
                        : {}),
                    },
                    onChange,
                  )
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="DoNotSchedule">DoNotSchedule（硬性）</SelectItem>
                  <SelectItem value="ScheduleAnyway">ScheduleAnyway（尽量）</SelectItem>
                </SelectContent>
              </Select>
            </Labeled>
            <Labeled label="最少拓扑域" hint="仅硬性策略可填；留空使用 Kubernetes 默认值">
              <NumericInput
                value={value.min_domains ? String(value.min_domains) : ""}
                disabled={value.when_unsatisfiable !== "DoNotSchedule"}
                onValueChange={(minDomains) =>
                  updateAt(
                    values,
                    index,
                    { ...value, min_domains: minDomains === "" ? undefined : Number(minDomains) },
                    onChange,
                  )
                }
              />
            </Labeled>
            <PolicySelect
              label="节点亲和性纳入策略"
              value={value.node_affinity_policy || DEFAULT_OPTION}
              onChange={(node_affinity_policy) =>
                updateAt(
                  values,
                  index,
                  {
                    ...value,
                    node_affinity_policy:
                      node_affinity_policy === DEFAULT_OPTION
                        ? undefined
                        : (node_affinity_policy as "Honor" | "Ignore"),
                  },
                  onChange,
                )
              }
            />
            <PolicySelect
              label="节点污点纳入策略"
              value={value.node_taints_policy || DEFAULT_OPTION}
              onChange={(node_taints_policy) =>
                updateAt(
                  values,
                  index,
                  {
                    ...value,
                    node_taints_policy:
                      node_taints_policy === DEFAULT_OPTION
                        ? undefined
                        : (node_taints_policy as "Honor" | "Ignore"),
                  },
                  onChange,
                )
              }
            />
          </div>

          <SelectorToggle
            label="Pod 标签选择器"
            hint="限定参与这条分布约束的 Pod；未启用表示不指定选择器。"
            value={value.label_selector}
            onChange={(label_selector) =>
              updateAt(values, index, { ...value, label_selector }, onChange)
            }
          />
          <StringListEditor
            label="动态匹配标签键"
            hint="从新 Pod 的标签取值加入上面的选择器。"
            values={value.match_label_keys ?? []}
            addLabel="添加匹配键"
            placeholder="例如 pod-template-hash"
            onChange={(match_label_keys) =>
              updateAt(values, index, { ...value, match_label_keys }, onChange)
            }
          />
        </RuleCard>
      ))}
      {values.length === 0 ? <EmptyHint>未配置拓扑分布约束。</EmptyHint> : null}
    </div>
  );
}

function SelectorToggle({
  label,
  hint,
  value,
  onChange,
}: {
  label: string;
  hint: string;
  value?: WorkloadLabelSelectorDraft;
  onChange: (value: WorkloadLabelSelectorDraft | undefined) => void;
}) {
  const enabled = value !== undefined;
  return (
    <div className="border-border rounded-control border p-3">
      <label className="flex items-start gap-2">
        <Checkbox
          checked={enabled}
          onCheckedChange={(checked) => onChange(checked === true ? {} : undefined)}
        />
        <span className="grid gap-0.5">
          <span className="text-foreground text-[13px] font-medium">{label}</span>
          <span className="text-subtle-foreground text-xs">{hint}</span>
        </span>
      </label>
      {enabled ? (
        <div className="mt-3 grid gap-3">
          <MatchLabelRows
            rows={Object.entries(value.match_labels ?? {}).map(([key, itemValue]) => ({
              key,
              value: itemValue,
            }))}
            onChange={(rows) =>
              onChange({
                ...value,
                match_labels: Object.fromEntries(rows.map((row) => [row.key, row.value])),
              })
            }
          />
          <RequirementRows
            label="标签表达式"
            hint="表达式之间是 AND。"
            rows={value.match_expressions ?? []}
            onChange={(match_expressions) =>
              onChange({
                ...value,
                match_expressions: match_expressions as WorkloadSelectorRequirementDraft[],
              })
            }
          />
        </div>
      ) : null}
    </div>
  );
}

function RequirementRows({
  label,
  hint,
  rows,
  node = false,
  onChange,
}: {
  label: string;
  hint: string;
  rows: (WorkloadNodeSelectorRequirementDraft | WorkloadSelectorRequirementDraft)[];
  node?: boolean;
  onChange: (rows: WorkloadNodeSelectorRequirementDraft[]) => void;
}) {
  const update = (index: number, row: WorkloadNodeSelectorRequirementDraft) =>
    onChange(rows.map((item, position) => (position === index ? row : item)));
  return (
    <div className="grid gap-2">
      <div>
        <p className="text-foreground text-[13px] font-medium">{label}</p>
        <p className="text-subtle-foreground text-xs">{hint}</p>
      </div>
      {rows.map((row, index) => {
        const acceptsValues =
          row.operator === "In" ||
          row.operator === "NotIn" ||
          row.operator === "Gt" ||
          row.operator === "Lt";
        return (
          <div
            key={index}
            className="border-border bg-surface-muted rounded-control grid gap-2 border p-2"
          >
            <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(8rem,0.7fr)_auto]">
              <Input
                value={row.key}
                aria-label={`${label} ${index + 1} 的键`}
                autoComplete="off"
                spellCheck={false}
                placeholder={node ? "例如 kubernetes.io/os" : "标签键"}
                onChange={(event) => update(index, { ...row, key: event.target.value })}
              />
              <Select
                value={row.operator}
                onValueChange={(operator) =>
                  update(index, {
                    ...row,
                    operator: operator as WorkloadNodeSelectorRequirementDraft["operator"],
                    values:
                      operator === "Exists" || operator === "DoesNotExist"
                        ? []
                        : row.values?.length
                          ? row.values
                          : [""],
                  })
                }
              >
                <SelectTrigger aria-label={`${label} ${index + 1} 的运算符`}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="In">In</SelectItem>
                  <SelectItem value="NotIn">NotIn</SelectItem>
                  <SelectItem value="Exists">Exists</SelectItem>
                  <SelectItem value="DoesNotExist">DoesNotExist</SelectItem>
                  {node ? <SelectItem value="Gt">Gt</SelectItem> : null}
                  {node ? <SelectItem value="Lt">Lt</SelectItem> : null}
                </SelectContent>
              </Select>
              <RemoveButton
                label={`移除${label} ${index + 1}`}
                onClick={() => onChange(rows.filter((_, position) => position !== index))}
              />
            </div>
            {acceptsValues ? (
              <StringListEditor
                label="匹配值"
                values={row.values ?? []}
                addLabel="添加匹配值"
                placeholder={row.operator === "Gt" || row.operator === "Lt" ? "整数" : "标签值"}
                onChange={(values) => update(index, { ...row, values })}
              />
            ) : (
              <p className="text-subtle-foreground text-xs">该运算符不接受匹配值。</p>
            )}
          </div>
        );
      })}
      <div>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => onChange([...rows, { key: "", operator: "In", values: [""] }])}
        >
          <Plus />
          添加条件
        </Button>
      </div>
    </div>
  );
}

function MatchLabelRows({
  rows,
  onChange,
}: {
  rows: KeyValueDraft[];
  onChange: (rows: KeyValueDraft[]) => void;
}) {
  return (
    <div className="grid gap-2">
      <p className="text-foreground text-[13px] font-medium">精确匹配标签</p>
      {rows.map((row, index) => (
        <div key={index} className="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
          <Input
            value={row.key}
            aria-label={`精确匹配标签 ${index + 1} 的键`}
            placeholder="标签键"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) =>
              onChange(
                rows.map((item, position) =>
                  position === index ? { ...item, key: event.target.value } : item,
                ),
              )
            }
          />
          <Input
            value={row.value}
            aria-label={`精确匹配标签 ${index + 1} 的值`}
            placeholder="标签值"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) =>
              onChange(
                rows.map((item, position) =>
                  position === index ? { ...item, value: event.target.value } : item,
                ),
              )
            }
          />
          <RemoveButton
            label={`移除精确匹配标签 ${index + 1}`}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          />
        </div>
      ))}
      <div>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => onChange([...rows, { key: "", value: "" }])}
        >
          <Plus />
          添加精确匹配标签
        </Button>
      </div>
    </div>
  );
}

function StringListEditor({
  label,
  hint,
  values,
  addLabel,
  placeholder,
  onChange,
}: {
  label: string;
  hint?: string;
  values: string[];
  addLabel: string;
  placeholder: string;
  onChange: (values: string[]) => void;
}) {
  return (
    <div className="grid content-start gap-2">
      <div>
        <p className="text-foreground text-xs font-medium">{label}</p>
        {hint ? <p className="text-subtle-foreground text-xs">{hint}</p> : null}
      </div>
      {values.map((value, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] gap-2">
          <Input
            value={value}
            aria-label={`${label} ${index + 1}`}
            autoComplete="off"
            spellCheck={false}
            placeholder={placeholder}
            onChange={(event) =>
              onChange(
                values.map((item, position) => (position === index ? event.target.value : item)),
              )
            }
          />
          <RemoveButton
            label={`移除${label} ${index + 1}`}
            onClick={() => onChange(values.filter((_, position) => position !== index))}
          />
        </div>
      ))}
      <div>
        <Button size="sm" variant="secondary" onClick={() => onChange([...values, ""])}>
          <Plus />
          {addLabel}
        </Button>
      </div>
    </div>
  );
}

function PolicySelect({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <Labeled label={label} hint="默认由 Kubernetes 决定">
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={DEFAULT_OPTION}>默认</SelectItem>
          <SelectItem value="Honor">Honor（纳入）</SelectItem>
          <SelectItem value="Ignore">Ignore（忽略）</SelectItem>
        </SelectContent>
      </Select>
    </Labeled>
  );
}

function RuleGroupHeader({
  title,
  hint,
  addLabel,
  onAdd,
}: {
  title: string;
  hint: string;
  addLabel: string;
  onAdd: () => void;
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-2">
      <div>
        <h5 className="text-foreground text-[13px] font-medium">{title}</h5>
        <p className="text-subtle-foreground text-xs">{hint}</p>
      </div>
      <Button size="sm" variant="secondary" onClick={onAdd}>
        <Plus />
        {addLabel}
      </Button>
    </div>
  );
}

function RuleCard({
  title,
  onRemove,
  children,
}: {
  title: string;
  onRemove: () => void;
  children: React.ReactNode;
}) {
  return (
    <section className="border-border bg-surface rounded-panel shadow-e1 grid gap-3 border p-3">
      <div className="flex items-center justify-between gap-2">
        <h6 className="text-foreground text-[13px] font-medium">{title}</h6>
        <RemoveButton label={`移除${title}`} onClick={onRemove} />
      </div>
      {children}
    </section>
  );
}

function Labeled({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label>{label}</Label>
      {children}
      {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
    </div>
  );
}

function EmptyHint({ children }: { children: React.ReactNode }) {
  return (
    <div className="border-border text-subtle-foreground rounded-control border border-dashed px-3 py-2 text-xs">
      {children}
    </div>
  );
}

function RuleCount({ value }: { value: number }) {
  return value > 0 ? (
    <span className="bg-surface-muted text-muted-foreground zke-tnum rounded-full px-1.5 text-[11px]">
      {value}
    </span>
  ) : null;
}

function RemoveButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button size="icon-sm" variant="ghost" aria-label={label} onClick={onClick}>
      <X />
    </Button>
  );
}

function emptyNodeTerm(): WorkloadNodeSelectorTermDraft {
  return { match_expressions: [{ key: "", operator: "In", values: [""] }] };
}

function emptyPodTerm(): WorkloadPodAffinityTermDraft {
  return { topology_key: "kubernetes.io/hostname" };
}

function emptyTopologySpread(): WorkloadTopologySpreadConstraintDraft {
  return {
    max_skew: 1,
    topology_key: "topology.kubernetes.io/zone",
    when_unsatisfiable: "DoNotSchedule",
  };
}

function updateAt<T>(values: T[], index: number, value: T, onChange: (values: T[]) => void) {
  onChange(values.map((item, position) => (position === index ? value : item)));
}
