import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { ExternalLink, LayoutGrid, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import {
  useCreateCustomApplication,
  useCustomApplications,
  useDeleteCustomApplication,
  useUpdateCustomApplication,
} from "@/api/queries/custom-applications";
import type { CustomApplication, CustomApplicationRequest } from "@/api/types";
import { AppGlyph } from "@/apps/AppGlyph";
import { appFaceClass } from "@/apps/accent";
import {
  AppShell,
  PageHeader,
  ScopeRequired,
  SectionTitle,
  SectionToolbarActions,
} from "@/apps/AppShell";
import { createCustomApplicationManifest, customApplicationManifestId } from "@/apps/registry";
import type { AppComponentProps } from "@/apps/types";
import { ErrorAlert } from "@/components/common/error-alert";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { Input, Textarea } from "@/components/ui/input";
import { FieldError, FieldHint, Label } from "@/components/ui/label";
import { Card } from "@/components/ui/misc";
import { useSessionContext } from "@/auth/session-context";
import { cn } from "@/lib/cn";
import { useSubmissionKey } from "@/lib/use-submission-key";
import { useScopeStore } from "@/scope/scope-store";

const NAV = [{ id: "applications", label: "自定义应用", icon: LayoutGrid }];
const APPLICATION_EDITOR_FORM_ID = "custom-application-editor-form";
const MAX_LOGO_DATA_URL_BYTES = 64 * 1024;
const LOGO_DATA_URL_PATTERN = /^data:image\/(?:jpeg|png|webp|gif|avif);base64,([a-z\d+/]+={0,2})$/i;

type EditorState = { mode: "create" } | { mode: "edit"; application: CustomApplication };

export function CustomApplicationsApp({ openApp }: AppComponentProps) {
  const scope = useScopeStore((state) => state.scope);
  const { permissions } = useSessionContext();
  const canManage = permissions.can("application.manage", {
    type: "project",
    tenantId: scope.tenantId,
    projectId: scope.projectId,
  });
  const query = useCustomApplications(scope.projectId);
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<CustomApplication | null>(null);
  const deleteMutation = useDeleteCustomApplication();

  if (!scope.projectId) {
    return <ScopeRequired />;
  }
  const projectId = scope.projectId;

  const openApplication = (application: CustomApplication) =>
    openApp(customApplicationManifestId(application.project_id, application.id), {
      title: application.name,
    });

  return (
    <AppShell
      nav={NAV}
      activeId="applications"
      onNavigate={() => {}}
      toolbar={
        <div className="min-w-0">
          <p className="text-foreground truncate text-[13px] font-medium">自定义应用</p>
          <p className="text-subtle-foreground truncate text-xs">
            {scope.projectName ?? scope.projectId}
          </p>
        </div>
      }
      scope={`项目：${scope.projectName ?? scope.projectId}`}
      statusBar={
        query.data ? `${query.data.applications.length} / ${query.data.limit} 个应用` : undefined
      }
    >
      {editor ? (
        <ApplicationEditor
          key={editor.mode === "create" ? "create" : editor.application.id}
          projectId={projectId}
          state={editor}
          onBack={() => setEditor(null)}
        />
      ) : (
        <>
          {canManage ? (
            <SectionToolbarActions>
              <Button size="sm" variant="primary" onClick={() => setEditor({ mode: "create" })}>
                <Plus aria-hidden />
                添加应用
              </Button>
            </SectionToolbarActions>
          ) : null}
          <SectionTitle
            title="项目应用"
            description="这里的入口由项目统一配置；拥有该项目读取权限的用户都会在桌面上看到。"
          />
          {query.isPending ? <LoadingState label="正在加载自定义应用…" /> : null}
          {query.isError ? (
            <ErrorState error={query.error} onRetry={() => query.refetch()} />
          ) : null}
          {query.data && query.data.applications.length === 0 ? (
            <EmptyState
              title="还没有自定义应用"
              description="可将 Harbor、Grafana 等 HTTP(S) 系统配置成项目桌面应用。"
              action={
                canManage ? (
                  <Button size="sm" variant="primary" onClick={() => setEditor({ mode: "create" })}>
                    <Plus aria-hidden />
                    添加第一个应用
                  </Button>
                ) : undefined
              }
            />
          ) : null}
          {query.data && query.data.applications.length > 0 ? (
            <div className="grid gap-3 @md:grid-cols-2 @3xl:grid-cols-3">
              {query.data.applications.map((application) => {
                const appManifest = createCustomApplicationManifest(application);
                return (
                  <Card key={application.id} className="flex min-w-0 flex-col gap-3">
                    <div className="flex min-w-0 items-start gap-3">
                      <span
                        className={cn(
                          "grid size-12 shrink-0 place-items-center rounded-[15px]",
                          appFaceClass(appManifest),
                          "zke-tile",
                        )}
                      >
                        <AppGlyph manifest={appManifest} className="size-8" />
                      </span>
                      <div className="min-w-0 flex-1">
                        <h4 className="text-foreground truncate text-sm font-semibold">
                          {application.name}
                        </h4>
                        <p className="text-subtle-foreground mt-0.5 truncate text-xs">
                          {new URL(application.url).host}
                        </p>
                      </div>
                    </div>
                    <p className="text-muted-foreground line-clamp-2 min-h-10 text-[13px] leading-5">
                      {application.description || "项目自定义应用"}
                    </p>
                    <div className="mt-auto flex flex-wrap items-center gap-2">
                      <Button
                        size="sm"
                        variant="primary"
                        onClick={() => openApplication(application)}
                      >
                        打开
                      </Button>
                      <Button size="sm" variant="secondary" asChild>
                        <a href={application.url} target="_blank" rel="noreferrer">
                          <ExternalLink aria-hidden />
                          新标签页
                        </a>
                      </Button>
                      {canManage ? (
                        <Button
                          size="icon-sm"
                          variant="ghost"
                          aria-label={`编辑 ${application.name}`}
                          onClick={() => setEditor({ mode: "edit", application })}
                        >
                          <Pencil aria-hidden />
                        </Button>
                      ) : null}
                      {canManage ? (
                        <Button
                          size="icon-sm"
                          variant="ghost"
                          className="text-danger hover:text-danger"
                          aria-label={`删除 ${application.name}`}
                          onClick={() => setDeleteTarget(application)}
                        >
                          <Trash2 aria-hidden />
                        </Button>
                      ) : null}
                    </div>
                  </Card>
                );
              })}
            </div>
          ) : null}
        </>
      )}

      <SensitiveActionDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => {
          if (!open) {
            setDeleteTarget(null);
            deleteMutation.reset();
          }
        }}
        title="删除自定义应用"
        description="只删除 ZKE 中的项目入口，不会修改目标系统。"
        scopeLines={[
          { label: "Project", name: scope.projectName ?? scope.projectId, id: scope.projectId },
          { label: "应用", name: deleteTarget?.name ?? "" },
        ]}
        impacts={["所有拥有该项目读取权限的用户都将不再看到这个应用入口。"]}
        confirmationText={deleteTarget?.name}
        confirmLabel="删除应用"
        destructive
        pending={deleteMutation.isPending}
        error={deleteMutation.error}
        onConfirm={() => {
          if (!deleteTarget) return;
          deleteMutation.mutate(
            { projectId, applicationId: deleteTarget.id },
            {
              onSuccess: () => {
                toast.success(`应用 ${deleteTarget.name} 已删除`);
                setDeleteTarget(null);
              },
            },
          );
        }}
      />
    </AppShell>
  );
}

function ApplicationEditor({
  projectId,
  state,
  onBack,
}: {
  projectId: string;
  state: EditorState;
  onBack: () => void;
}) {
  const existing = state.mode === "edit" ? state.application : null;
  const [name, setName] = useState(existing?.name ?? "");
  const [description, setDescription] = useState(existing?.description ?? "");
  const [url, setURL] = useState(existing?.url ?? "");
  const [logoURL, setLogoURL] = useState(existing?.logo_url ?? "");
  const [validationError, setValidationError] = useState<string | null>(null);
  const createMutation = useCreateCustomApplication();
  const updateMutation = useUpdateCustomApplication();
  const submissionKey = useSubmissionKey(true);
  const mutation = existing ? updateMutation : createMutation;

  const preview = useMemo<CustomApplication>(
    () => ({
      id: existing?.id ?? "00000000-0000-0000-0000-000000000000",
      project_id: projectId,
      name: name.trim() || "应用预览",
      description: description.trim(),
      url: url.trim() || "https://example.invalid",
      logo_url: logoURL.trim(),
      created_at: existing?.created_at ?? new Date(0).toISOString(),
      updated_at: existing?.updated_at ?? new Date(0).toISOString(),
    }),
    [description, existing, logoURL, name, projectId, url],
  );

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const application: CustomApplicationRequest = {
      name: name.trim(),
      description: description.trim(),
      url: url.trim(),
      logo_url: logoURL.trim(),
    };
    const invalid = validateApplication(application);
    setValidationError(invalid);
    if (invalid) return;

    if (existing) {
      updateMutation.mutate(
        { projectId, applicationId: existing.id, application },
        {
          onSuccess: () => {
            toast.success("应用配置已保存");
            onBack();
          },
        },
      );
    } else {
      createMutation.mutate(
        { projectId, application, idempotencyKey: submissionKey },
        {
          onSuccess: () => {
            toast.success(`应用 ${application.name} 已添加`);
            onBack();
          },
        },
      );
    }
  };

  return (
    <form id={APPLICATION_EDITOR_FORM_ID} onSubmit={submit} className="max-w-4xl">
      <PageHeader
        title={existing ? `编辑 ${existing.name}` : "添加自定义应用"}
        onBack={onBack}
        backDisabled={mutation.isPending}
        actions={
          // PageHeader portals actions into the AppShell header, outside this
          // form in the DOM. The explicit form owner keeps native validation
          // and submit handling connected across that portal boundary.
          <Button
            type="submit"
            form={APPLICATION_EDITOR_FORM_ID}
            size="sm"
            variant="primary"
            disabled={mutation.isPending}
          >
            {mutation.isPending ? "保存中…" : "保存"}
          </Button>
        }
      />
      <div className="grid gap-5">
        <div className="grid gap-4 @md:grid-cols-[1fr_12rem]">
          <div className="grid gap-4">
            <Field label="应用名称" htmlFor="custom-app-name" required>
              <Input
                id="custom-app-name"
                value={name}
                maxLength={80}
                autoFocus
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            <Field label="应用 URL" htmlFor="custom-app-url" required>
              <Input
                id="custom-app-url"
                type="url"
                value={url}
                placeholder="https://harbor.example.com"
                onChange={(event) => setURL(event.target.value)}
              />
              <FieldHint>
                仅配置可信应用；兼容嵌入会在浏览器中执行目标应用脚本。不要在 URL 中放用户名、密码或
                Token。
              </FieldHint>
            </Field>
            <Field label="Logo URL / Data URL" htmlFor="custom-app-logo-url">
              <Input
                id="custom-app-logo-url"
                type="text"
                inputMode="url"
                value={logoURL}
                maxLength={MAX_LOGO_DATA_URL_BYTES}
                placeholder="https://example.com/logo.svg 或 data:image/jpeg;base64,…"
                onChange={(event) => setLogoURL(event.target.value)}
              />
              <FieldHint>
                可选。外链限 2048 字节；JPEG、PNG、WebP、GIF、AVIF 的 Base64 Data URL 限 64 KiB。
              </FieldHint>
            </Field>
          </div>
          <Card className="flex min-h-44 flex-col items-center justify-center gap-3 text-center">
            <span
              className={cn(
                "grid size-20 place-items-center rounded-[25px]",
                appFaceClass(createCustomApplicationManifest(preview)),
                "zke-tile",
              )}
            >
              <AppGlyph manifest={createCustomApplicationManifest(preview)} className="size-12" />
            </span>
            <p className="text-foreground max-w-full truncate text-[13px] font-medium">
              {name.trim() || "应用预览"}
            </p>
          </Card>
        </div>
        <Field label="说明" htmlFor="custom-app-description">
          <Textarea
            id="custom-app-description"
            value={description}
            maxLength={500}
            placeholder="说明这个入口连接的系统和用途"
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        <div aria-live="polite">
          {validationError ? <FieldError>{validationError}</FieldError> : null}
          <ErrorAlert error={mutation.error} />
        </div>
      </div>
    </form>
  );
}

function Field({
  label,
  htmlFor,
  required,
  children,
}: {
  label: string;
  htmlFor: string;
  required?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={htmlFor}>
        {label}
        {required ? <span className="text-danger"> *</span> : null}
      </Label>
      {children}
    </div>
  );
}

function validateApplication(input: CustomApplicationRequest): string | null {
  if (!input.name) return "请输入应用名称";
  if (byteLength(input.name) > 80) return "应用名称不能超过 80 字节";
  if (byteLength(input.description ?? "") > 500) return "应用说明不能超过 500 字节";
  const invalidApplicationURL = validateHTTPURL(input.url, "应用 URL");
  if (invalidApplicationURL) return invalidApplicationURL;
  const invalidLogoURL = validateLogoURL(input.logo_url ?? "");
  if (invalidLogoURL) return invalidLogoURL;
  return null;
}

function validateLogoURL(raw: string): string | null {
  if (!raw) return null;
  if (!raw.toLowerCase().startsWith("data:")) return validateHTTPURL(raw, "Logo URL");
  if (byteLength(raw) > MAX_LOGO_DATA_URL_BYTES) return "Logo Data URL 不能超过 64 KiB";

  const match = LOGO_DATA_URL_PATTERN.exec(raw);
  if (!match) return "Logo Data URL 仅支持 JPEG、PNG、WebP、GIF 或 AVIF 的 Base64 图片";
  const payload = match[1];
  if (!payload) return "Logo Data URL 必须包含有效的 Base64 数据";
  try {
    atob(payload);
  } catch {
    return "Logo Data URL 必须包含有效的 Base64 数据";
  }
  return null;
}

function validateHTTPURL(raw: string, label: string): string | null {
  if (byteLength(raw) > 2048) return `${label} 不能超过 2048 字节`;
  try {
    const parsed = new URL(raw);
    if (
      !(["http:", "https:"] as string[]).includes(parsed.protocol) ||
      parsed.username ||
      parsed.password
    ) {
      return `${label} 必须是无用户凭证的绝对 HTTP(S) 地址`;
    }
  } catch {
    return `${label} 必须是有效的绝对地址`;
  }
  return null;
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}
