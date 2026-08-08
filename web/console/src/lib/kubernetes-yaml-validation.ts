export type KubernetesYamlIdentity = {
  apiVersion: string;
  kind: string;
  namespace?: string;
  name: string;
  uid: string;
  resourceVersion: string;
};

export type KubernetesYamlIssue = {
  message: string;
  line?: number;
  column?: number;
};

export type KubernetesYamlValidation = {
  valid: boolean;
  issues: KubernetesYamlIssue[];
  identity?: KubernetesYamlIdentity;
};

const MAX_ISSUES = 20;

function issueFromParser(error: {
  message: string;
  linePos?: { line: number; col: number }[];
}): KubernetesYamlIssue {
  const position = error.linePos?.[0];
  return {
    message: error.message.replace(/ at line \d+, column \d+:/u, ":"),
    ...(position ? { line: position.line, column: position.col } : {}),
  };
}

function objectValue(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/**
 * Strict, local preflight for one Kubernetes object.
 *
 * This does not claim to know the target Cluster's schemas or admission policy;
 * the Server's Kubernetes DryRun remains authoritative. Its purpose is to catch
 * malformed YAML, ambiguous YAML features and changed identity while the cursor
 * is still in the editor, before a network request or audit event is needed.
 */
export async function validateKubernetesYaml(
  source: string,
  expected?: KubernetesYamlIdentity,
): Promise<KubernetesYamlValidation> {
  if (source.trim() === "") {
    return { valid: false, issues: [{ message: "YAML 文档不能为空。" }] };
  }

  let documents;
  try {
    const { parseAllDocuments } = await import("yaml");
    documents = parseAllDocuments(source, {
      prettyErrors: true,
      strict: true,
      uniqueKeys: true,
      version: "1.2",
    });
  } catch (error) {
    return {
      valid: false,
      issues: [{ message: error instanceof Error ? error.message : "YAML 解析失败。" }],
    };
  }
  const issues = documents.flatMap((document) => document.errors.map(issueFromParser));
  const nonEmpty = documents.filter((document) => document.contents !== null);
  if (nonEmpty.length !== 1) {
    issues.push({ message: "必须且只能包含一个非空 YAML 文档。" });
  }
  const document = nonEmpty[0];
  if (!document) {
    return { valid: false, issues: issues.slice(0, MAX_ISSUES) };
  }
  const { isAlias, isMap, isNode, visit } = await import("yaml");
  if (!isMap(document.contents)) {
    issues.push({ message: "Kubernetes 对象的 YAML 顶层必须是映射。" });
  }

  visit(document, (_key, node) => {
    if (issues.length >= MAX_ISSUES) return visit.BREAK;
    if (!isNode(node)) return undefined;
    if (isAlias(node)) {
      issues.push({ message: "不允许 YAML alias；请展开引用内容。" });
      return visit.SKIP;
    }
    if ("anchor" in node && typeof node.anchor === "string" && node.anchor !== "") {
      issues.push({ message: "不允许 YAML anchor；请展开复用内容。" });
    }
    if ("tag" in node && typeof node.tag === "string" && node.tag.startsWith("!")) {
      issues.push({ message: "不允许自定义 YAML tag。" });
    }
    return undefined;
  });

  let value: unknown;
  try {
    value = document.toJS({ maxAliasCount: 0 });
  } catch (error) {
    issues.push({ message: error instanceof Error ? error.message : "YAML 结构无法转换。" });
  }
  const object = objectValue(value);
  const metadata = objectValue(object?.metadata);
  const identity: KubernetesYamlIdentity = {
    apiVersion: stringValue(object?.apiVersion),
    kind: stringValue(object?.kind),
    namespace: stringValue(metadata?.namespace) || undefined,
    name: stringValue(metadata?.name),
    uid: stringValue(metadata?.uid),
    resourceVersion: stringValue(metadata?.resourceVersion),
  };
  for (const [label, field] of [
    ["apiVersion", identity.apiVersion],
    ["kind", identity.kind],
    ["metadata.name", identity.name],
    ["metadata.uid", identity.uid],
    ["metadata.resourceVersion", identity.resourceVersion],
  ] as const) {
    if (!field) issues.push({ message: `缺少必填身份字段 ${label}。` });
  }
  if (expected) {
    for (const [label, current, submitted] of [
      ["apiVersion", expected.apiVersion, identity.apiVersion],
      ["kind", expected.kind, identity.kind],
      ["metadata.name", expected.name, identity.name],
      ["metadata.namespace", expected.namespace ?? "", identity.namespace ?? ""],
      ["metadata.uid", expected.uid, identity.uid],
      ["metadata.resourceVersion", expected.resourceVersion, identity.resourceVersion],
    ] as const) {
      if (current !== submitted) {
        issues.push({ message: `${label} 必须保持为 ${current || "<空>"}。` });
      }
    }
  }
  return { valid: issues.length === 0, issues: issues.slice(0, MAX_ISSUES), identity };
}
