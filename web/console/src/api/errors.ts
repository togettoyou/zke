import type { components } from "./schema";

export type ErrorBody = components["schemas"]["Error"];

/**
 * Every ZKE Server error carries a stable code, a safe message and a request
 * correlation id. The Console shows a localized message plus the request id so
 * an operator can find the matching audit event and server log.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string;
  readonly retryAfterSeconds: number | null;

  constructor(options: {
    status: number;
    code: string;
    message: string;
    requestId: string;
    retryAfterSeconds?: number | null;
  }) {
    super(options.message);
    this.name = "ApiError";
    this.status = options.status;
    this.code = options.code;
    this.requestId = options.requestId;
    this.retryAfterSeconds = options.retryAfterSeconds ?? null;
  }

  get localizedMessage(): string {
    const prefix = DETAILED_ERROR_PREFIXES[this.code];
    if (prefix !== undefined && this.message !== "") {
      return `${prefix}${this.message}`;
    }
    return ERROR_MESSAGES[this.code] ?? ERROR_MESSAGES[String(this.status)] ?? this.message;
  }
}

export function isApiError(error: unknown): error is ApiError {
  return error instanceof ApiError;
}

export function isUnauthenticated(error: unknown): boolean {
  return isApiError(error) && error.status === 401;
}

export function isForbidden(error: unknown): boolean {
  return isApiError(error) && error.status === 403;
}

/**
 * Codes whose message is the answer, and must not be replaced by a general one.
 *
 * Kubernetes says exactly which field it refused and why — a NodePort outside
 * the range this cluster was configured with, a value another controller owns —
 * and that sentence is the only place the reason appears. It is kept verbatim,
 * in Kubernetes' own wording, behind a line saying where it came from; a
 * translation would have to guess at text this Console has never seen.
 */
const DETAILED_ERROR_PREFIXES: Record<string, string> = {
  cluster_api_rejected: "Kubernetes 拒绝了该配置：",
};

export const ERROR_MESSAGES: Record<string, string> = {
  // Authentication and session
  unauthenticated: "登录状态已失效，请重新登录",
  invalid_credentials: "用户名或密码不正确",
  csrf_invalid: "安全校验失败，请重新登录后重试",
  cross_origin_forbidden: "跨源请求被拒绝，请通过同源入口访问 Console",
  invalid_current_password: "当前密码不正确",
  invalid_new_password: "新密码不满足密码策略（至少 15 个字符）",
  password_unchanged: "新密码不能与当前密码相同",

  // Authorization
  forbidden: "当前账号没有执行该操作的权限",

  // Request validation
  invalid_request: "请求内容无效，请检查输入",
  // The fallback for a rejection that arrived without the API Server's account;
  // when it has one, DETAILED_ERROR_PREFIXES shows it instead.
  cluster_api_rejected: "Kubernetes 拒绝了该配置，请检查字段取值",
  confirmation_required: "该操作需要显式确认",
  invalid_yaml: "YAML 无效，请检查文档结构和资源身份",
  manifest_too_large: "YAML 超过 4 MiB，请缩减内容后重试",
  unsupported_media_type: "请求正文必须使用 application/yaml",

  // Lifecycle and idempotency
  not_found: "目标不存在，或不在当前权限范围内",
  // Not a malformed request: the container has simply never restarted, or the
  // node no longer keeps the log of the instance before this one.
  previous_logs_not_found: "该容器没有上一个实例的日志：它没有重启过，或上一个实例的日志已被清理",
  resource_conflict: "资源状态与请求冲突",
  resource_state_conflict: "目标资源当前状态不允许该操作",
  idempotency_conflict: "幂等键已用于内容不同的请求，请重新发起",
  resource_uid_changed: "资源已被删除并以同名重新创建，请重新读取 YAML",
  resource_version_changed: "资源已发生变化，请重新读取 YAML 后合并修改",
  // Each spells out its own scope and says where the holder might be hiding: a
  // stopped resource still holds its name and is not in the default list.
  tenant_name_conflict:
    "租户名称已被占用（不区分大小写；已停用的租户仍然占用其名称，删除才会释放）",
  project_name_conflict:
    "同一租户下项目名称已被占用（不区分大小写；已停用的项目仍然占用其名称，删除才会释放）",
  cluster_name_conflict:
    "同一项目下集群名称已被占用（不区分大小写；已停用的集群或未使用的接入凭证都会占用名称）",
  last_global_admin: "必须保留最后一个有效的全局管理员",
  self_disable_forbidden: "不能禁用当前登录的账号",
  self_delete_forbidden: "不能删除当前登录的账号",
  token_rejected: "凭证无效或已被使用",
  credential_rejected: "接入凭证已失效或被撤销",

  // Transport and availability
  too_many_requests: "请求过于频繁，请稍后再试",
  timeout: "请求处理超时，请稍后重试",
  unavailable: "服务暂不可用，请稍后重试",
  internal_error: "服务内部错误，请联系管理员并提供请求 ID",
  network_error: "无法连接到 ZKE Server，请检查网络或服务状态",

  // Status fallbacks
  "400": "请求内容无效",
  "401": "登录状态已失效，请重新登录",
  "403": "当前账号没有执行该操作的权限",
  "404": "目标不存在，或不在当前权限范围内",
  "409": "资源状态与请求冲突",
  "413": "请求正文过大",
  "415": "请求正文格式不受支持",
  "429": "请求过于频繁，请稍后再试",
  "500": "服务内部错误",
  "503": "服务暂不可用",
  "504": "请求处理超时",
};

/** Message suitable for direct display, without the request id. */
export function errorMessage(error: unknown): string {
  if (isApiError(error)) {
    return error.localizedMessage;
  }
  if (error instanceof Error && error.name === "AbortError") {
    return "请求已取消或超时";
  }
  return ERROR_MESSAGES.network_error as string;
}

/**
 * The Server's stable machine-readable code, for the few callers that recover
 * from a specific failure rather than just reporting it.
 */
export function errorCode(error: unknown): string | null {
  return isApiError(error) && error.code ? error.code : null;
}

/** Request id, when the failure actually reached the Server. */
export function errorRequestId(error: unknown): string | null {
  return isApiError(error) && error.requestId ? error.requestId : null;
}
