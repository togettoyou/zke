import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Plus } from "lucide-react";

import {
  useCreateHelmRepository,
  useDeleteHelmRepository,
  useHelmRepositories,
  useUpdateHelmRepository,
} from "@/api/queries/helm";
import type { HelmRepository } from "@/api/types";
import { SectionToolbarActions } from "@/apps/AppShell";
import { DataTable } from "@/components/common/data-table";
import { RowDeleteAction } from "@/components/common/delete-action";
import { ErrorAlert } from "@/components/common/error-alert";
import { notifyFailure } from "@/components/common/notify";
import { RefreshAction } from "@/components/common/refresh-action";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { CREDENTIAL_MANAGER_IGNORE, Input, Textarea } from "@/components/ui/input";
import { Alert } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

import { Field, FieldGrid, SwitchField } from "./form";

type SignaturePolicy = HelmRepository["signature_policy"];

/**
 * What a repository requires of a chart's provenance.
 *
 * The index digest only says the repository is serving what it published; it
 * says nothing about who produced the archive. Helm publishes a detached PGP
 * signature beside each chart, and this is the platform's decision about
 * whether to insist on one — per repository, because a repository is one
 * publisher and its keys belong to it.
 */
const SIGNATURE_POLICIES: {
  value: NonNullable<SignaturePolicy>;
  label: string;
  hint: string;
}[] = [
  {
    value: "disabled",
    label: "不校验",
    hint: "不获取也不校验来源证明。公开仓库通常不签名。",
  },
  {
    value: "verify_if_present",
    label: "有签名则校验",
    hint: "仓库发布了 .prov 就校验，没有发布则放行。这是发布方推进签名期间的过渡状态，不是安全边界——能替换归档的一方也能删掉它旁边的文件。",
  },
  {
    value: "required",
    label: "必须签名",
    hint: "拒绝一切无法归因到下方密钥的归档。安装与浏览同样适用。",
  },
];

function signaturePolicyLabel(policy: SignaturePolicy): string {
  return SIGNATURE_POLICIES.find((entry) => entry.value === policy)?.label ?? "不校验";
}

/**
 * The chart catalogue an administrator curates.
 *
 * Adding a repository is what decides where this Server will make outbound
 * requests, which is why it needs `helm.repository.manage` and why no other
 * route in the application accepts a URL. Removing one leaves every installed
 * release untouched: a release carries the chart it was installed from.
 *
 * A credential is write-only. It is stored, sent upstream, and never returned —
 * the table shows that one is configured, never what it is.
 */
export function RepositorySection({ canManage }: { canManage: boolean }) {
  const repositories = useHelmRepositories();
  const [editing, setEditing] = useState<HelmRepository | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<HelmRepository | null>(null);
  const remove = useDeleteHelmRepository();

  const columns = useMemo<ColumnDef<HelmRepository, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium break-all">{row.original.name}</span>
            <span className="text-subtle-foreground text-xs break-all">
              {row.original.description || "—"}
            </span>
          </div>
        ),
      },
      {
        header: "地址",
        cell: ({ row }) => (
          <span className="zke-mono text-muted-foreground text-xs break-all">
            {row.original.url}
          </span>
        ),
      },
      {
        header: "状态",
        size: 200,
        cell: ({ row }) => (
          <div className="flex flex-wrap gap-1">
            <Badge tone={row.original.enabled ? "success" : "neutral"}>
              {row.original.enabled ? "已启用" : "已停用"}
            </Badge>
            {row.original.has_credentials ? <Badge tone="info">已配置凭证</Badge> : null}
            {row.original.insecure_skip_tls_verify ? (
              <Badge tone="warning">跳过 TLS 校验</Badge>
            ) : null}
            {/* A repository that checks signatures says so here, with how many
                keys it checks against: "必须签名" and "有签名则校验" are
                different guarantees, and a keyring is what either rests on. */}
            {row.original.signature_policy && row.original.signature_policy !== "disabled" ? (
              <Badge tone={row.original.signature_policy === "required" ? "success" : "info"}>
                {signaturePolicyLabel(row.original.signature_policy)}·
                {row.original.signing_keys?.length ?? 0} 把密钥
              </Badge>
            ) : null}
          </div>
        ),
      },
      ...(canManage
        ? [
            {
              id: "actions",
              header: "",
              size: 120,
              cell: ({ row }) => (
                <div className="flex justify-end gap-1">
                  <Button size="sm" variant="ghost" onClick={() => setEditing(row.original)}>
                    编辑
                  </Button>
                  {/* The button opens the confirmation; it does not delete.
                      Wiring it straight to the mutation is one click away from
                      removing a repository — and its stored credential — for
                      everyone on the platform. */}
                  <RowDeleteAction
                    name={row.original.name}
                    hint="从平台目录中移除该仓库。已安装的 Release 不受影响。"
                    onDelete={() => setDeleting(row.original)}
                  />
                </div>
              ),
            } satisfies ColumnDef<HelmRepository, unknown>,
          ]
        : []),
    ],
    [canManage],
  );

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <SectionToolbarActions>
        <RefreshAction
          isFetching={repositories.isFetching}
          onRefresh={() => void repositories.refetch()}
        />
        {canManage ? (
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus />
            添加仓库
          </Button>
        ) : null}
      </SectionToolbarActions>
      {canManage ? null : (
        <Alert tone="info">
          你可以查看平台上的 Chart 仓库，但添加、修改与删除需要 helm.repository.manage。
        </Alert>
      )}
      <DataTable
        columns={columns}
        data={repositories.data?.repositories}
        isLoading={repositories.isLoading}
        isFetching={repositories.isFetching}
        error={repositories.error}
        onRetry={() => void repositories.refetch()}
        rowKey={(repository) => repository.id}
        emptyTitle="还没有 Chart 仓库"
        emptyDescription="Chart 只能来自这里配置的仓库；应用中没有任何接受任意地址的入口。"
        /* The first thing to do on an empty catalogue is add to it, so the way
           to do that is here as well as in the toolbar: a first-run screen whose
           only action lives in a corner of a chrome row reads as a dead end. */
        emptyAction={
          canManage ? (
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus />
              添加仓库
            </Button>
          ) : undefined
        }
      />
      <RepositoryDialog
        open={creating}
        onOpenChange={setCreating}
        repository={null}
        onDone={() => setCreating(false)}
      />
      <RepositoryDialog
        open={editing !== null}
        onOpenChange={(open) => setEditing(open ? editing : null)}
        repository={editing}
        onDone={() => setEditing(null)}
      />
      <SensitiveActionDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null);
            remove.reset();
          }
        }}
        title={`删除 Chart 仓库 ${deleting?.name ?? ""}`}
        description="这是平台级的目录条目：删除之后，所有人的新安装都不能再从它选择 Chart。"
        scopeLines={[
          { label: "仓库", name: deleting?.name ?? "", id: deleting?.id },
          { label: "地址", name: deleting?.url ?? "" },
        ]}
        impacts={[
          "该仓库不再出现在 Chart 目录中，新的安装与升级不能再从它选择 Chart",
          deleting?.has_credentials
            ? "为它配置的凭证一并删除，重新添加时需要再次填写"
            : "该仓库没有配置凭证",
          "已安装的 Release 不受影响：Release 自带安装时使用的 Chart",
        ]}
        confirmationText={deleting?.name}
        confirmLabel="删除"
        destructive
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() => {
          if (!deleting) return;
          remove.mutate(deleting.id, {
            onSuccess: () => setDeleting(null),
            onError: (error) => notifyFailure("删除 Chart 仓库", error),
          });
        }}
      />
    </div>
  );
}

/**
 * Add or edit one repository.
 *
 * It stays a dialog rather than becoming a page: six fields, no list, nothing to
 * scroll, and nothing an operator has to go and look up halfway through.
 */
function RepositoryDialog({
  open,
  onOpenChange,
  repository,
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  repository: HelmRepository | null;
  onDone: () => void;
}) {
  const create = useCreateHelmRepository();
  const update = useUpdateHelmRepository();
  const mutation = repository ? update : create;

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [url, setUrl] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  // Editing starts with the password untouched: the value was never sent to the
  // browser, so leaving the field empty has to mean "keep it" rather than
  // "clear it". Clearing is a separate, explicit choice.
  const [replacePassword, setReplacePassword] = useState(!repository);
  const [caCertificate, setCaCertificate] = useState("");
  const [insecure, setInsecure] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [signaturePolicy, setSignaturePolicy] = useState<NonNullable<SignaturePolicy>>("disabled");
  // The keyring is public material and is returned in full, so editing starts
  // from what is stored: adding one key to three means submitting all four, and
  // an administrator cannot retype what they were never shown.
  const [publicKeyring, setPublicKeyring] = useState("");

  // Reset during render when the dialog opens, matching how the other forms in
  // this Console clear their fields: a stale value must never be readable, not
  // even for the frame between opening and an effect running.
  const [wasOpen, setWasOpen] = useState(open);
  if (wasOpen !== open) {
    setWasOpen(open);
    if (open) {
      setName(repository?.name ?? "");
      setDescription(repository?.description ?? "");
      setUrl(repository?.url ?? "");
      setUsername(repository?.username ?? "");
      setPassword("");
      setReplacePassword(!repository);
      setCaCertificate(repository?.ca_certificate_pem ?? "");
      setInsecure(repository?.insecure_skip_tls_verify ?? false);
      setEnabled(repository?.enabled ?? true);
      setSignaturePolicy(repository?.signature_policy ?? "disabled");
      setPublicKeyring(repository?.public_keyring ?? "");
      mutation.reset();
    }
  }

  const submit = () => {
    const body = {
      name: name.trim(),
      description: description.trim(),
      url: url.trim(),
      username: username.trim(),
      // Omitted entirely when it is not being replaced, which the Server reads
      // as "keep what is stored".
      ...(replacePassword ? { password } : {}),
      ca_certificate_pem: caCertificate.trim(),
      insecure_skip_tls_verify: insecure,
      enabled,
      signature_policy: signaturePolicy,
      public_keyring: publicKeyring.trim(),
    };
    const onSettled = {
      onSuccess: onDone,
      onError: (error: unknown) =>
        notifyFailure(repository ? "更新 Chart 仓库" : "添加 Chart 仓库", error),
    };
    if (repository) {
      update.mutate({ repositoryId: repository.id, body }, onSettled);
    } else {
      create.mutate(body, onSettled);
    }
  };

  return (
    <Dialog open={open} onOpenChange={mutation.isPending ? () => {} : onOpenChange}>
      <DialogContent className="w-[min(640px,calc(100vw-2rem))]">
        {/* `autoComplete="off"` on the form is honoured by Firefox and Safari
            for the whole set; Chrome needs the per-field values below. */}
        <form
          autoComplete="off"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <DialogHeader>
            <DialogTitle>{repository ? "编辑 Chart 仓库" : "添加 Chart 仓库"}</DialogTitle>
            <DialogDescription>
              仓库地址决定 Server 会向哪里发起请求，因此只有持有 helm.repository.manage
              的管理员可以配置它。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-3 py-2">
            <FieldGrid>
              <Field label="名称" htmlFor="helm-repository-name">
                <Input
                  id="helm-repository-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="bitnami"
                  autoComplete="off"
                  spellCheck={false}
                  maxLength={100}
                  required
                />
              </Field>
              <Field label="说明" htmlFor="helm-repository-description">
                <Input
                  id="helm-repository-description"
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  autoComplete="off"
                  maxLength={500}
                />
              </Field>
            </FieldGrid>
            <Field
              label="地址"
              htmlFor="helm-repository-url"
              hint="绝对的 http 或 https 地址，`index.yaml` 位于其下；不要把用户名和口令写进 URL。"
            >
              <Input
                id="helm-repository-url"
                value={url}
                onChange={(event) => setUrl(event.target.value)}
                placeholder="https://charts.example.com"
                autoComplete="off"
                spellCheck={false}
                maxLength={2000}
                required
              />
            </Field>

            {/*
             * The repository's credential, which is not the operator's own.
             *
             * A browser fills a password field from what it saved for this site,
             * and it picks the text field just above it as the username — which
             * is exactly this pair. `new-password` is the one value Chrome and
             * Safari honour to mean "this is not a login", and the ignore
             * attributes keep the password managers from offering to save a
             * registry credential into the operator's vault afterwards. The
             * names are deliberately not `username`/`password` for the same
             * reason: they are what the heuristics match on.
             */}
            <FieldGrid>
              <Field label="用户名" htmlFor="helm-repository-username" hint="私有仓库才需要。">
                <Input
                  id="helm-repository-username"
                  name="helm-repository-account"
                  value={username}
                  onChange={(event) => setUsername(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  maxLength={200}
                  {...CREDENTIAL_MANAGER_IGNORE}
                />
              </Field>
              <Field
                label="口令"
                htmlFor="helm-repository-password"
                hint={
                  repository
                    ? repository.has_credentials
                      ? "已存储一个口令，不勾选下方则保留它。"
                      : "该仓库没有存储口令。"
                    : "写入后任何接口都不再返回它。"
                }
              >
                <Input
                  id="helm-repository-password"
                  name="helm-repository-secret"
                  type="password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  disabled={!replacePassword}
                  placeholder={replacePassword ? "" : "保持不变"}
                  autoComplete="new-password"
                  maxLength={1000}
                  {...CREDENTIAL_MANAGER_IGNORE}
                />
              </Field>
            </FieldGrid>
            {repository ? (
              <SwitchField
                id="helm-repository-replace-password"
                checked={replacePassword}
                onChange={(value) => {
                  setReplacePassword(value);
                  if (!value) setPassword("");
                }}
                label="替换口令"
                hint="不勾选表示保留已存储的口令；勾选后留空表示清除它。"
              />
            ) : null}

            <Field
              label="自定义 CA（PEM）"
              htmlFor="helm-repository-ca"
              hint={
                repository?.ca_certificate_provided
                  ? "已配置自定义 CA，内容如下；清空并保存表示不再使用它。"
                  : "留空表示使用系统信任库。"
              }
            >
              <Textarea
                id="helm-repository-ca"
                value={caCertificate}
                onChange={(event) => setCaCertificate(event.target.value)}
                rows={3}
                spellCheck={false}
                className="zke-mono text-xs"
                placeholder="-----BEGIN CERTIFICATE-----"
              />
            </Field>

            {/*
             * Chart provenance.
             *
             * The two fields are one decision and are shown together: a policy
             * other than 「不校验」 with no keys behind it is refused by the
             * Server, because it would reject every chart under 「必须签名」 and
             * — worse — admit every chart under 「有签名则校验」 while this page
             * still read as verification.
             */}
            <div className="grid gap-3">
              <Field
                label="Chart 签名校验"
                htmlFor="helm-repository-signature-policy"
                hint={
                  SIGNATURE_POLICIES.find((entry) => entry.value === signaturePolicy)?.hint ?? ""
                }
              >
                <Select
                  value={signaturePolicy}
                  onValueChange={(value) =>
                    setSignaturePolicy(value as NonNullable<SignaturePolicy>)
                  }
                >
                  <SelectTrigger id="helm-repository-signature-policy" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SIGNATURE_POLICIES.map((entry) => (
                      <SelectItem key={entry.value} value={entry.value}>
                        {entry.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              {signaturePolicy === "disabled" ? null : (
                <Field
                  label="签名公钥（PGP，ASCII-armored）"
                  htmlFor="helm-repository-keyring"
                  hint="该仓库的 Chart 允许由哪些密钥签名。可以粘贴多把密钥；公钥不是机密，会原样返回以便编辑。"
                >
                  <Textarea
                    id="helm-repository-keyring"
                    value={publicKeyring}
                    onChange={(event) => setPublicKeyring(event.target.value)}
                    rows={4}
                    spellCheck={false}
                    className="zke-mono text-xs"
                    placeholder="-----BEGIN PGP PUBLIC KEY BLOCK-----"
                  />
                </Field>
              )}
              {repository?.signing_keys?.length ? (
                <div className="text-subtle-foreground grid gap-1 text-xs">
                  {repository.signing_keys.map((key) => (
                    <div key={key.fingerprint} className="flex flex-wrap items-baseline gap-2">
                      {/* The fingerprint, because it is the only part of a PGP
                          key that identifies it — a user ID is free text its
                          own owner wrote. */}
                      <span className="zke-mono text-foreground break-all">{key.fingerprint}</span>
                      <span className="break-all">{key.identities.join("、") || "—"}</span>
                    </div>
                  ))}
                </div>
              ) : null}
            </div>

            <div className="grid gap-3">
              <SwitchField
                id="helm-repository-insecure"
                checked={insecure}
                onChange={setInsecure}
                label="跳过 TLS 校验"
                hint="Server 将不再验证该仓库的证书。这是一次明确且可审计的选择，会显示在列表中。"
                tone="warning"
              />
              <SwitchField
                id="helm-repository-enabled"
                checked={enabled}
                onChange={setEnabled}
                label="启用"
                hint="停用后不再用于新的安装；已安装的 Release 不受影响。"
              />
            </div>
            {mutation.error ? <ErrorAlert error={mutation.error} /> : null}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={mutation.isPending}
            >
              取消
            </Button>
            <Button type="submit" disabled={mutation.isPending}>
              {repository ? "保存" : "添加"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
