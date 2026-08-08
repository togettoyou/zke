import { useState, type ReactNode } from "react";
import { AlertTriangle } from "lucide-react";

import { errorMessage, errorRequestId } from "@/api/errors";
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
import { Alert } from "@/components/ui/misc";

export type ScopeLine = {
  label: string;
  name: string;
  id?: string | null;
};

export type SensitiveActionDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  /** Full target scope: what exactly this operation will act on. */
  scopeLines: ScopeLine[];
  /** Concrete, user-visible consequences of confirming. */
  impacts: ReactNode[];
  /** When set, the operator must type this value (usually the resource name). */
  confirmationText?: string;
  confirmLabel?: string;
  destructive?: boolean;
  pending?: boolean;
  error?: unknown;
  children?: ReactNode;
  /** Optional sizing override for review-heavy actions such as a YAML diff. */
  contentClassName?: string;
  onConfirm: () => void;
};

/**
 * Single confirmation surface for every sensitive operation.
 *
 * It shows the action, the exact target scope, the impact list and — for
 * destructive operations — requires the resource name to be typed. The Server
 * still performs its own RBAC check, confirmation flag validation and audit
 * write; this dialog exists so the operator sees what they are about to do.
 */
export function SensitiveActionDialog({
  open,
  onOpenChange,
  title,
  description,
  scopeLines,
  impacts,
  confirmationText,
  confirmLabel = "确认执行",
  destructive = false,
  pending = false,
  error,
  children,
  contentClassName,
  onConfirm,
}: SensitiveActionDialogProps) {
  const [typed, setTyped] = useState("");
  const [wasOpen, setWasOpen] = useState(open);

  // Adjust state during render rather than in an effect: a stale confirmation
  // string must never survive into the next time the dialog opens.
  if (wasOpen !== open) {
    setWasOpen(open);
    if (!open) {
      setTyped("");
    }
  }

  const confirmationSatisfied = !confirmationText || typed.trim() === confirmationText;
  const requestId = errorRequestId(error);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent aria-describedby={undefined} className={contentClassName}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {destructive ? (
              <AlertTriangle className="text-danger size-4 shrink-0" aria-hidden />
            ) : null}
            {title}
          </DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>

        <section className="border-border bg-surface-muted rounded-panel border p-3">
          <h4 className="text-subtle-foreground mb-2 text-xs font-medium">操作目标</h4>
          <dl className="grid gap-1.5">
            {scopeLines.map((line) => (
              <div key={line.label} className="flex items-start gap-2 text-[13px]">
                {/*
                 * `min-w-20` rather than `w-20`: the labels are resource kinds,
                 * and a kind longer than the column — ResourceQuota,
                 * ClusterRoleBinding — overflowed a fixed width and ran straight
                 * into the value, because a single word has nowhere to wrap.
                 */}
                <dt className="text-muted-foreground min-w-20 shrink-0 leading-6">{line.label}</dt>
                <dd className="min-w-0 flex-1">
                  <div className="text-foreground leading-6 font-medium">{line.name}</div>
                  {/*
                   * The identifier gets its own line, and is never abbreviated.
                   * This is the surface where an operator checks that they are
                   * about to act on the thing they meant to — so the whole value
                   * has to be here, and trailed after the name it had nowhere to
                   * go but a mid-token break, which is the one way of wrapping an
                   * identifier that makes it unreadable. On its own line a UUID
                   * fits; `break-all` stays only as a floor for longer ones.
                   */}
                  {line.id ? (
                    <div className="zke-mono text-subtle-foreground text-xs break-all">
                      {line.id}
                    </div>
                  ) : null}
                </dd>
              </div>
            ))}
          </dl>
        </section>

        {impacts.length > 0 ? (
          <section className="mt-3">
            <h4 className="text-subtle-foreground mb-1.5 text-xs font-medium">影响</h4>
            <ul className="text-foreground list-disc space-y-1 pl-5 text-[13px]">
              {impacts.map((impact, index) => (
                <li key={index}>{impact}</li>
              ))}
            </ul>
          </section>
        ) : null}

        {children ? <div className="mt-3">{children}</div> : null}

        {confirmationText ? (
          <div className="mt-4 grid gap-1.5">
            <Label htmlFor="sensitive-action-confirmation">
              请输入 <span className="zke-mono text-foreground">{confirmationText}</span> 以确认
            </Label>
            <Input
              id="sensitive-action-confirmation"
              value={typed}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => setTyped(event.target.value)}
            />
          </div>
        ) : null}

        {error ? (
          <Alert tone="danger" className="mt-3">
            {errorMessage(error)}
            {requestId ? (
              <span className="zke-mono mt-1 block text-xs opacity-80">请求 ID：{requestId}</span>
            ) : null}
          </Alert>
        ) : null}

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={pending}>
            取消
          </Button>
          <Button
            variant={destructive ? "danger" : "primary"}
            disabled={!confirmationSatisfied || pending}
            onClick={onConfirm}
          >
            {pending ? "执行中…" : confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
