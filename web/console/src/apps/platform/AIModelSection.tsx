import { useState } from "react";
import { toast } from "sonner";

import {
  useAIModelSettings,
  useSetAIModelEnabled,
  useTestAIModelSettings,
  useUpdateAIModelSettings,
} from "@/api/queries/platform-settings";
import type { AIModelSettings, AIModelSettingsUpdate } from "@/api/types";
import { errorMessage } from "@/api/errors";
import { ErrorAlert } from "@/components/common/error-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CREDENTIAL_MANAGER_IGNORE, Input, NumericInput } from "@/components/ui/input";
import { FieldHint, Label } from "@/components/ui/label";
import { Alert, Checkbox, Switch } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

/**
 * What the form holds, which is not what the Server stores.
 *
 * `apiKey` and `clearAPIKey` exist because the stored credential is never
 * returned: an empty field means "leave it alone", so clearing has to be said
 * out loud rather than by emptying an input that was already empty.
 */
type Draft = {
  baseURL: string;
  model: string;
  apiProtocol: AIModelSettings["api_protocol"];
  contextWindowTokens: string;
  maxOutputTokens: string;
  timeoutSeconds: string;
  apiKey: string;
  clearAPIKey: boolean;
};

function draftOf(settings: AIModelSettings): Draft {
  return {
    baseURL: settings.base_url,
    model: settings.model,
    apiProtocol: settings.api_protocol,
    contextWindowTokens: String(settings.context_window_tokens),
    maxOutputTokens: String(settings.max_output_tokens),
    timeoutSeconds: String(settings.request_timeout_seconds),
    apiKey: "",
    clearAPIKey: false,
  };
}

function isDirty(draft: Draft, settings: AIModelSettings): boolean {
  return (
    draft.baseURL !== settings.base_url ||
    draft.model !== settings.model ||
    draft.apiProtocol !== settings.api_protocol ||
    draft.contextWindowTokens !== String(settings.context_window_tokens) ||
    draft.maxOutputTokens !== String(settings.max_output_tokens) ||
    draft.timeoutSeconds !== String(settings.request_timeout_seconds) ||
    draft.apiKey !== "" ||
    draft.clearAPIKey
  );
}

/**
 * The same rules as `aimodel.normalizeSettingsInput`, stated a second time so a
 * refusal the browser can already see does not cost a round trip. The Server
 * checks all of it again and wins wherever the two disagree.
 */
function problemsOf(draft: Draft, enabled: boolean): string[] {
  const problems: string[] = [];
  const baseURL = draft.baseURL.trim();
  const model = draft.model.trim();
  if (enabled && (baseURL === "" || model === "")) {
    problems.push("清空接入地址或模型名前请先关闭 AIOps");
  }
  if (baseURL !== "" && !/^https?:\/\/\S+$/.test(baseURL)) {
    problems.push("接入地址必须以 http:// 或 https:// 开头");
  }
  if (/\s/.test(model)) {
    problems.push("模型名不能包含空白字符");
  }
  const contextWindow = Number(draft.contextWindowTokens);
  const maxOutput = Number(draft.maxOutputTokens);
  if (!Number.isInteger(contextWindow) || contextWindow < 16384 || contextWindow > 4000000) {
    problems.push("上下文窗口必须是 16384 至 4000000 tokens");
  }
  if (!Number.isInteger(maxOutput) || maxOutput < 1024 || maxOutput > 262144) {
    problems.push("单次最大输出必须是 1024 至 262144 tokens");
  }
  if (maxOutput >= contextWindow) {
    problems.push("单次最大输出必须小于上下文窗口");
  }
  const timeout = Number(draft.timeoutSeconds);
  if (!Number.isInteger(timeout) || timeout < 5 || timeout > 300) {
    problems.push("请求超时必须是 5 至 300 秒的整秒数");
  }
  return problems;
}

const FAILURE_LABELS: Record<string, string> = {
  unauthorized: "认证失败",
  model_not_found: "模型或路径不存在",
  unreachable: "连接失败",
  timeout: "超时",
  unexpected_response: "响应异常",
};

/**
 * Where the assistant's model endpoint is configured.
 *
 * Shipped off. Nothing about the assistant is reachable until this is filled
 * in, which is why the section explains what enabling it means for data leaving
 * ZKE before it asks for an address.
 *
 * It carries its own draft and its own save rather than joining the other
 * sections' shared one: the endpoint has its own revision, because it is read
 * and written on its own.
 */
export function AIModelSection() {
  const query = useAIModelSettings();
  const update = useUpdateAIModelSettings();
  const setEnabled = useSetAIModelEnabled();
  const test = useTestAIModelSettings();
  const [draft, setDraft] = useState<Draft | null>(null);

  const settings = query.data ?? null;
  if (query.isLoading) {
    return <p className="text-muted-foreground text-sm">正在加载模型接入配置…</p>;
  }
  if (!settings) {
    return <Alert tone="danger">{errorMessage(query.error)}</Alert>;
  }

  const form = draft ?? draftOf(settings);
  const problems = problemsOf(form, settings.enabled);
  const dirty = isDirty(form, settings);

  function change(next: Partial<Draft>) {
    setDraft({ ...form, ...next });
  }

  async function save() {
    if (!settings) {
      return;
    }
    const body: AIModelSettingsUpdate = {
      base_url: form.baseURL.trim(),
      model: form.model.trim(),
      api_protocol: form.apiProtocol,
      context_window_tokens: Number(form.contextWindowTokens),
      max_output_tokens: Number(form.maxOutputTokens),
      request_timeout_seconds: Number(form.timeoutSeconds),
      expected_revision: settings.revision,
    };
    // Absent keeps the stored key, empty clears it. A field the operator did
    // not touch must be absent, because there is nothing to send back.
    if (form.clearAPIKey) {
      body.api_key = "";
    } else if (form.apiKey !== "") {
      body.api_key = form.apiKey;
    }
    try {
      await update.mutateAsync(body);
      test.reset();
      setDraft(null);
      toast.success("模型接入配置已保存");
    } catch {
      // Reported below the button by ErrorAlert.
    }
  }

  // The result is announced rather than only rendered: the button sits at the
  // foot of a long form, and an answer that only appears under it is an answer
  // the operator has to go looking for. The alert below stays as the readable
  // record of the last test.
  async function runTest() {
    try {
      const result = await test.mutateAsync();
      if (result.succeeded) {
        toast.success("连通性测试通过");
      } else {
        toast.error(`${FAILURE_LABELS[result.failure] ?? "连通性测试失败"}：${result.detail}`);
      }
    } catch (error) {
      toast.error(errorMessage(error));
    }
  }

  const outcome = test.data;
  const testable = settings.base_url !== "" && settings.model !== "";
  // The switch acts on what is stored, not on what the form currently holds:
  // turning AIOps on with no saved endpoint is refused by the Server with a
  // 409, which reads as "somebody else changed this" rather than "there is
  // nothing to turn on". Refusing it here says the actual reason, next to the
  // fields that fix it.
  const enableBlocked = !settings.enabled && !testable;

  return (
    <div className="grid gap-6">
      <section>
        <h4 className="text-foreground mb-1 text-[13px] font-semibold">模型接入</h4>
        <p className="text-muted-foreground mb-3 text-xs leading-relaxed">
          AIOps 调用的模型端点。支持 OpenAI Responses 与 Chat
          Completions，可以指向自建推理服务。请求由 Server 发起，不经过
          Agent，目标集群不需要任何出向能力。此处保存并测试 AIOps App 使用的模型配置。
        </p>
        <Alert tone="warning" className="mb-4">
          启用后，AIOps 从集群取到的内容会被发送到这里配置的模型端点：资源对象摘要、Kubernetes
          Event、指标查询结果、Pod
          日志与终端输出。日志和命令输出可能包含业务敏感信息；在用户持有相应权限并按
          会话审批模式放行时，Secret 内容也可能进入当前任务上下文。
        </Alert>
        {/*
         * 这一组的每个输入框都带 CREDENTIAL_MANAGER_IGNORE，不只是 API Key 那个：
         * 浏览器与密码管理器把「password 框前面的那个文本框」认作用户名，只标记密码框
         * 会让接入地址或模型名被填进别人的登录凭证。
         */}
        <div className="grid gap-5">
          <div className="flex items-start justify-between gap-4">
            <div className="grid gap-1">
              <Label htmlFor="ai-model-enabled">启用 AIOps</Label>
              <FieldHint>
                {enableBlocked
                  ? "尚未保存模型接入地址与模型名，暂时不能启用。填写下面的字段并保存后再打开。"
                  : settings.enabled && !settings.api_key_configured
                    ? "已启用，但还没有 API Key：默认接入地址需要凭证，在下面填写并保存后 AIOps 才能真正回答。"
                    : "关闭后保留配置，并停止新的 AIOps 模型运行；未启用时桌面上不显示 AIOps 应用。"}
              </FieldHint>
            </div>
            <Switch
              id="ai-model-enabled"
              checked={settings.enabled}
              disabled={setEnabled.isPending || enableBlocked}
              onCheckedChange={(checked) =>
                void setEnabled
                  .mutateAsync({ enabled: checked, expectedRevision: settings.revision })
                  .then(() => toast.success(checked ? "AIOps 已启用" : "AIOps 已关闭"))
                  .catch(() => undefined)
              }
            />
          </div>
          <ErrorAlert error={setEnabled.error} />
          <div className="grid gap-1.5">
            <Label htmlFor="ai-model-base-url">接入地址</Label>
            <Input
              id="ai-model-base-url"
              value={form.baseURL}
              placeholder="https://api.deepseek.com"
              autoComplete="off"
              spellCheck={false}
              {...CREDENTIAL_MANAGER_IGNORE}
              onChange={(event) => change({ baseURL: event.target.value })}
            />
            <FieldHint>
              OpenAI 兼容端点的 Base URL，通常以 /v1 结尾。ZKE 按所选协议追加操作路径。
            </FieldHint>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="ai-model-name">模型名</Label>
            <Input
              id="ai-model-name"
              value={form.model}
              placeholder="deepseek-v4-flash"
              autoComplete="off"
              spellCheck={false}
              {...CREDENTIAL_MANAGER_IGNORE}
              onChange={(event) => change({ model: event.target.value })}
            />
            <FieldHint>由管理员填写，ZKE 不内置任何厂商模型清单。</FieldHint>
          </div>
          <div className="grid max-w-md gap-1.5">
            <Label htmlFor="ai-model-protocol">API 协议</Label>
            <Select
              value={form.apiProtocol}
              onValueChange={(value) =>
                change({ apiProtocol: value as AIModelSettings["api_protocol"] })
              }
            >
              <SelectTrigger id="ai-model-protocol">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="responses">Responses API（推荐）</SelectItem>
                <SelectItem value="chat_completions">Chat Completions</SelectItem>
              </SelectContent>
            </Select>
            <FieldHint>
              Responses 更适合长任务和工具调用；仅提供 /chat/completions 的自建服务选择后者。
            </FieldHint>
          </div>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-2">
              <Label htmlFor="ai-model-api-key">API Key</Label>
              <Badge tone={settings.api_key_configured ? "success" : "neutral"}>
                {settings.api_key_configured ? "已配置" : "未配置"}
              </Badge>
            </div>
            <Input
              id="ai-model-api-key"
              type="password"
              // `off` is ignored on a credential field; `new-password` is the
              // documented way to say "this is not a login you saved".
              autoComplete="new-password"
              value={form.apiKey}
              disabled={form.clearAPIKey}
              placeholder={settings.api_key_configured ? "留空表示保持不变" : "内网服务可留空"}
              {...CREDENTIAL_MANAGER_IGNORE}
              onChange={(event) => change({ apiKey: event.target.value })}
            />
            <FieldHint>
              写入后不再回显，也不进入日志、审计与错误消息。内网自建推理服务不需要凭证时可以留空。
            </FieldHint>
            {settings.api_key_configured ? (
              <label className="text-muted-foreground mt-1 flex items-center gap-2 text-xs">
                <Checkbox
                  checked={form.clearAPIKey}
                  onCheckedChange={(checked) =>
                    change({ clearAPIKey: checked === true, apiKey: "" })
                  }
                />
                清除已保存的 API Key
              </label>
            ) : null}
          </div>
          <div className="grid max-w-xs gap-1.5">
            <Label htmlFor="ai-model-context-window">上下文窗口（tokens）</Label>
            <NumericInput
              id="ai-model-context-window"
              value={form.contextWindowTokens}
              onValueChange={(value) => change({ contextWindowTokens: value })}
            />
            <FieldHint>
              填写模型真实上限，例如 262144 或 1000000。自动压缩按 Server 配置中的比例乘以这个窗口
              计算触发点，换模型只需要改这里。
            </FieldHint>
          </div>
          <div className="grid max-w-xs gap-1.5">
            <Label htmlFor="ai-model-max-output">单次最大输出（tokens）</Label>
            <NumericInput
              id="ai-model-max-output"
              value={form.maxOutputTokens}
              onValueChange={(value) => change({ maxOutputTokens: value })}
            />
            <FieldHint>为模型回答、工具调用和推理结果预留的最大空间。</FieldHint>
          </div>
          <div className="grid max-w-xs gap-1.5">
            <Label htmlFor="ai-model-timeout">请求超时（秒）</Label>
            <NumericInput
              id="ai-model-timeout"
              value={form.timeoutSeconds}
              onValueChange={(value) => change({ timeoutSeconds: value })}
            />
            <FieldHint>单次模型调用的上限，5 至 300 秒。</FieldHint>
          </div>
        </div>
      </section>

      <div className="grid gap-2">
        {problems.length > 0 ? (
          <Alert tone="danger">
            <ul className="list-disc space-y-0.5 pl-4">
              {problems.map((problem) => (
                <li key={problem}>{problem}</li>
              ))}
            </ul>
          </Alert>
        ) : null}
        <ErrorAlert error={update.error} />
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            disabled={update.isPending || problems.length > 0 || !dirty}
            onClick={() => void save()}
          >
            {update.isPending ? "保存中…" : "保存模型接入"}
          </Button>
          <Button
            variant="secondary"
            disabled={test.isPending || !testable || dirty}
            onClick={() => void runTest()}
          >
            {test.isPending ? "测试中…" : "测试连通性"}
          </Button>
        </div>
        <FieldHint>
          {dirty
            ? "测试使用已保存的配置，请先保存本次修改。"
            : "测试会用已保存的配置向模型端点发起一次最小请求，不改变任何配置。"}
        </FieldHint>
        <ErrorAlert error={test.error} />
        {outcome ? (
          <Alert tone={outcome.succeeded ? "success" : "danger"}>
            {outcome.succeeded
              ? "连通性测试通过：模型端点返回了与所选协议匹配的响应。"
              : `${FAILURE_LABELS[outcome.failure] ?? "测试失败"}：${outcome.detail}${
                  outcome.status > 0 ? `（HTTP ${outcome.status}）` : ""
                }`}
          </Alert>
        ) : null}
      </div>
    </div>
  );
}
