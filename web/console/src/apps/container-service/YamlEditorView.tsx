import { useEffect, useRef, useState } from "react";
import { RotateCw, Save } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import type { Permission } from "@/api/types";
import { newIdempotencyKey } from "@/api/client";
import { useResourceYaml, useUpdateResourceYaml, type YamlTarget } from "@/api/queries/yaml";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { CopyButton } from "@/components/common/status";
import { ErrorState, LoadingState } from "@/components/common/state";
import { YamlEditor } from "@/components/common/yaml-editor";
import { YamlDiff } from "@/components/common/yaml-diff";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { useSubmissionKey } from "@/lib/use-submission-key";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import {
  validateKubernetesYaml,
  type KubernetesYamlValidation,
} from "@/lib/kubernetes-yaml-validation";

/** The Server refuses anything larger, so the editor says so before the round trip. */
const MAX_YAML_BYTES = 4 * 1024 * 1024;

/**
 * Reads and replaces one object's YAML.
 *
 * The document is shown exactly as Kubernetes returned it, including the
 * `metadata.uid` and `metadata.resourceVersion` the Server checks on write — an
 * editor that tidied those away would be removing the preconditions that stop an
 * edit of a stale read from reverting whatever changed in between.
 */
export function YamlEditorView({
  identity,
  clusterName,
  kindLabel,
  canUpdate,
  writePermission,
  readOnlyReason,
  impacts,
  onBack,
}: {
  identity: YamlTarget;
  clusterName: string;
  /** What to call this object in headings and confirmations, e.g. "Deployment". */
  kindLabel: string;
  canUpdate: boolean;
  /**
   * The permission this document's write actually answers to, named in the
   * read-only notice. Passed where it is not the one an operator would assume —
   * a Node answers to `cluster.node.manage`, not to `cluster.resource.update` —
   * so the notice says which grant to go and ask for rather than leaving it to
   * be guessed from the role editor.
   */
  writePermission?: Permission;
  /**
   * Why this document cannot be saved, when that is not about permissions —
   * an immutable Secret, one of ZKE's own objects. Stated rather than left to
   * a disabled button, and stated here rather than discovered on submit.
   */
  readOnlyReason?: string;
  /** Replaces the generic account of what a write does, where it differs. */
  impacts?: string[];
  onBack: () => void;
}) {
  const source = useResourceYaml(identity);
  const update = useUpdateResourceYaml();
  const [draft, setDraft] = useState<string | null>(null);
  const [previewed, setPreviewed] = useState<{ submitted: string; effective: string } | null>(null);
  // A failed DryRun must reuse its key when the same YAML is retried, but a
  // corrected document is a new request and must not collide with the old
  // request fingerprint under that key.
  const previewAttempt = useRef<{ yaml: string; key: string } | null>(null);
  const applyKey = useSubmissionKey(previewed !== null);

  const writable = canUpdate && readOnlyReason === undefined;
  const loaded = source.data?.yaml ?? "";
  // `null` means untouched, which is what tells a reload from a discarded edit.
  const text = draft ?? loaded;
  const dirty = draft !== null && draft !== loaded;
  const oversized = new Blob([text]).size > MAX_YAML_BYTES;
  const validationText = useDebouncedValue(text);
  const [loadedValidationState, setLoadedValidationState] = useState<{
    source: string;
    result: KubernetesYamlValidation;
  } | null>(null);
  const [validationState, setValidationState] = useState<{
    source: string;
    expectedSource: string;
    result: KubernetesYamlValidation;
  } | null>(null);
  const loadedValidation =
    loadedValidationState?.source === loaded ? loadedValidationState.result : null;
  const validation: KubernetesYamlValidation =
    loadedValidation !== null && !loadedValidation.valid
      ? loadedValidation
      : validationState?.source === validationText && validationState.expectedSource === loaded
        ? validationState.result
        : { valid: false, issues: [] };
  const validating =
    validationText !== text ||
    (loaded !== "" && loadedValidation === null) ||
    (loadedValidation?.valid === true &&
      (validationState?.source !== validationText || validationState.expectedSource !== loaded));

  useEffect(() => {
    let canceled = false;
    if (!loaded) return () => undefined;
    void validateKubernetesYaml(loaded).then((result) => {
      if (!canceled) setLoadedValidationState({ source: loaded, result });
    });
    return () => {
      canceled = true;
    };
  }, [loaded]);

  useEffect(() => {
    let canceled = false;
    if (!loadedValidation?.valid || !loadedValidation.identity) {
      return () => undefined;
    }
    void validateKubernetesYaml(validationText, loadedValidation.identity).then((result) => {
      if (!canceled) {
        setValidationState({ source: validationText, expectedSource: loaded, result });
      }
    });
    return () => {
      canceled = true;
    };
  }, [loaded, loadedValidation, validationText]);

  const reload = () => {
    setDraft(null);
    setPreviewed(null);
    previewAttempt.current = null;
    update.reset();
    void source.refetch();
  };

  const submit = (dryRun: boolean, yaml: string) => {
    let idempotencyKey = applyKey;
    if (dryRun) {
      if (previewAttempt.current?.yaml !== yaml) {
        previewAttempt.current = { yaml, key: newIdempotencyKey() };
      }
      idempotencyKey = previewAttempt.current.key;
    }
    void update
      .mutateAsync({
        target: identity,
        yaml,
        dryRun,
        idempotencyKey,
      })
      .then((result) => {
        if (dryRun) {
          setPreviewed({ submitted: yaml, effective: result.yaml });
          return;
        }
        toast.success(`${kindLabel} ${identity.name} 已更新`);
        setPreviewed(null);
        setDraft(null);
        previewAttempt.current = null;
        // Re-read rather than keep what was submitted: the object now carries a
        // new resourceVersion, and editing on top of the old one would conflict
        // on the very next save.
        void source.refetch();
      })
      .catch(() => undefined);
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* No description: the toolbar already names the Cluster and Namespace,
          and the DryRun is not a caveat to read but the step the 保存 button
          actually performs before anything is written. */}
      <PageHeader
        title={`${identity.name} · YAML`}
        onBack={onBack}
        backDisabled={update.isPending}
        actions={
          <>
            <CopyButton value={text} label="复制" />
            <Button
              size="sm"
              variant="secondary"
              onClick={reload}
              disabled={source.isFetching || update.isPending}
            >
              <RotateCw />
              {dirty ? "放弃修改并重新读取" : "重新读取"}
            </Button>
            {writable ? (
              <Button
                size="sm"
                variant="primary"
                disabled={
                  !dirty ||
                  oversized ||
                  validating ||
                  !validation.valid ||
                  update.isPending ||
                  source.isLoading
                }
                onClick={() => submit(true, text)}
              >
                <Save />
                {update.isPending ? "DryRun 预检中…" : "保存"}
              </Button>
            ) : null}
          </>
        }
      />

      {source.error ? (
        <ErrorState error={source.error} onRetry={() => void source.refetch()} />
      ) : source.isLoading ? (
        <LoadingState />
      ) : (
        <>
          {readOnlyReason ? (
            <Alert tone="warning" className="mb-3">
              {readOnlyReason}
            </Alert>
          ) : canUpdate ? null : (
            <Alert tone="info" className="mb-3">
              当前身份没有该集群的写入权限，YAML 只读。
              {writePermission ? `写入该对象需要 ${writePermission} 权限。` : null}
            </Alert>
          )}
          {oversized ? (
            <Alert tone="danger" className="mb-3">
              文档超过 4 MiB，服务端不会接受；请缩减内容后再保存。
            </Alert>
          ) : null}
          {dirty && !validating && !validation.valid ? (
            <Alert tone="danger" className="mb-3">
              <span className="font-medium">YAML 本地结构校验未通过：</span>
              <ul className="mt-1 list-disc space-y-0.5 pl-5">
                {validation.issues.map((issue, index) => (
                  <li key={`${issue.line ?? 0}-${issue.column ?? 0}-${index}`}>
                    {issue.line
                      ? `第 ${issue.line} 行${issue.column ? `第 ${issue.column} 列` : ""}：`
                      : ""}
                    {issue.message}
                  </li>
                ))}
              </ul>
            </Alert>
          ) : null}
          {update.error ? (
            <Alert tone="danger" className="mb-3">
              {errorMessage(update.error)}
            </Alert>
          ) : null}

          <YamlEditor
            value={text}
            onChange={(next) => {
              setDraft(next);
              setPreviewed(null);
              if (update.error) {
                update.reset();
              }
            }}
            // Keep the exact document sent to DryRun frozen until its result is
            // known; otherwise edits made in flight could leave the confirmation
            // dialog previewing an older document than the one on screen.
            readOnly={!writable || update.isPending || source.isFetching}
            label={`${identity.name} 的 YAML`}
            className="min-h-0 flex-1"
          />

          <div className="text-subtle-foreground mt-3 flex flex-wrap items-center gap-3 text-xs">
            <span className="zke-mono">resourceVersion {source.data?.resourceVersion || "—"}</span>
            <span className="zke-mono break-all">UID {source.data?.uid || "—"}</span>
            {dirty ? <span className="text-warning">有未保存的修改</span> : null}
            {dirty && validating ? <span>正在检查 YAML 结构…</span> : null}
            {dirty && !validating && validation.valid ? (
              <span className="text-success">本地结构与对象身份校验通过</span>
            ) : null}
          </div>
        </>
      )}

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={`确认更新 ${kindLabel}`}
        description="DryRun 预检已通过。确认后将向同一集群提交实际更新。"
        scopeLines={[
          { label: "集群", name: clusterName, id: identity.clusterId },
          ...(identity.namespace ? [{ label: "命名空间", name: identity.namespace }] : []),
          { label: kindLabel, name: identity.name, id: source.data?.uid },
        ]}
        impacts={
          impacts ?? [
            "整份文档会替换集群中的现有对象，未在文档中出现的字段将被移除。",
            "服务端会核对文档内的 UID 与 resourceVersion；期间对象若已变化，本次更新会被拒绝而不是覆盖。",
            "控制器可能因此重建 Pod 或触发滚动更新。",
          ]
        }
        confirmationText={identity.name}
        confirmLabel="确认更新"
        destructive
        pending={update.isPending}
        error={update.error}
        contentClassName="w-[min(1120px,calc(100vw-2rem))]"
        pinConfirmationControls
        onConfirm={() => previewed !== null && submit(false, previewed.submitted)}
      >
        {previewed ? <YamlDiff before={loaded} after={previewed.effective} /> : null}
      </SensitiveActionDialog>
    </div>
  );
}
