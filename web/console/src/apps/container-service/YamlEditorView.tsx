import { useRef, useState } from "react";
import { ArrowLeft, RotateCw, Save } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { newIdempotencyKey } from "@/api/client";
import { useResourceYaml, useUpdateResourceYaml, type ResourceIdentity } from "@/api/queries/yaml";
import { SectionTitle } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { CopyButton } from "@/components/common/status";
import { ErrorState, LoadingState } from "@/components/common/state";
import { YamlEditor } from "@/components/common/yaml-editor";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { useSubmissionKey } from "@/lib/use-submission-key";

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
  onBack,
}: {
  identity: ResourceIdentity;
  clusterName: string;
  /** What to call this object in headings and confirmations, e.g. "Deployment". */
  kindLabel: string;
  canUpdate: boolean;
  onBack: () => void;
}) {
  const source = useResourceYaml(identity);
  const update = useUpdateResourceYaml();
  const [draft, setDraft] = useState<string | null>(null);
  const [previewed, setPreviewed] = useState<string | null>(null);
  // A failed DryRun must reuse its key when the same YAML is retried, but a
  // corrected document is a new request and must not collide with the old
  // request fingerprint under that key.
  const previewAttempt = useRef<{ yaml: string; key: string } | null>(null);
  const applyKey = useSubmissionKey(previewed !== null);

  const loaded = source.data?.yaml ?? "";
  // `null` means untouched, which is what tells a reload from a discarded edit.
  const text = draft ?? loaded;
  const dirty = draft !== null && draft !== loaded;
  const oversized = new Blob([text]).size > MAX_YAML_BYTES;

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
        identity,
        yaml,
        dryRun,
        idempotencyKey,
      })
      .then(() => {
        if (dryRun) {
          setPreviewed(yaml);
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
      <SectionTitle
        title={`${identity.name} · YAML`}
        description={`读取自集群 ${clusterName}${identity.namespace ? ` 的 ${identity.namespace}` : ""}。保存前先执行 Kubernetes 服务端 DryRun。`}
        actions={
          <div className="flex items-center gap-2">
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
            {canUpdate ? (
              <Button
                size="sm"
                variant="primary"
                disabled={!dirty || oversized || update.isPending || source.isLoading}
                onClick={() => submit(true, text)}
              >
                <Save />
                {update.isPending ? "预检中…" : "保存"}
              </Button>
            ) : null}
            <Button size="sm" variant="secondary" onClick={onBack} disabled={update.isPending}>
              <ArrowLeft />
              返回
            </Button>
          </div>
        }
      />

      {source.error ? (
        <ErrorState error={source.error} onRetry={() => void source.refetch()} />
      ) : source.isLoading ? (
        <LoadingState />
      ) : (
        <>
          {canUpdate ? null : (
            <Alert tone="info" className="mb-3">
              当前身份没有该集群的 `cluster.resource.update` 权限，YAML 只读。
            </Alert>
          )}
          {oversized ? (
            <Alert tone="danger" className="mb-3">
              文档超过 4 MiB，服务端不会接受；请缩减内容后再保存。
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
              if (update.error) {
                update.reset();
              }
            }}
            // Keep the exact document sent to DryRun frozen until its result is
            // known; otherwise edits made in flight could leave the confirmation
            // dialog previewing an older document than the one on screen.
            readOnly={!canUpdate || update.isPending || source.isFetching}
            label={`${identity.name} 的 YAML`}
            className="min-h-0 flex-1"
          />

          <div className="text-subtle-foreground mt-3 flex flex-wrap items-center gap-3 text-xs">
            <span className="zke-mono">resourceVersion {source.data?.resourceVersion || "—"}</span>
            <span className="zke-mono break-all">UID {source.data?.uid || "—"}</span>
            {dirty ? <span className="text-warning">有未保存的修改</span> : null}
          </div>
        </>
      )}

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={`确认更新 ${kindLabel}`}
        description="DryRun 已通过。确认后将向同一集群提交实际更新。"
        scopeLines={[
          { label: "集群", name: clusterName, id: identity.clusterId },
          ...(identity.namespace ? [{ label: "命名空间", name: identity.namespace }] : []),
          { label: kindLabel, name: identity.name, id: source.data?.uid },
        ]}
        impacts={[
          "整份文档会替换集群中的现有对象，未在文档中出现的字段将被移除。",
          "服务端会核对文档内的 UID 与 resourceVersion；期间对象若已变化，本次更新会被拒绝而不是覆盖。",
          "控制器可能因此重建 Pod 或触发滚动更新。",
        ]}
        confirmationText={identity.name}
        confirmLabel="确认更新"
        destructive
        pending={update.isPending}
        error={update.error}
        onConfirm={() => previewed !== null && submit(false, previewed)}
      />
    </div>
  );
}
