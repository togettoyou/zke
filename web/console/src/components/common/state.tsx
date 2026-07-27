import type { ReactNode } from "react";
import { AlertTriangle, Inbox, Loader2, ShieldAlert } from "lucide-react";

import { errorMessage, errorRequestId, isForbidden } from "@/api/errors";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

export function LoadingState({ label = "加载中…" }: { label?: string }) {
  return (
    <div
      className="text-muted-foreground flex items-center justify-center gap-2 py-10 text-[13px]"
      role="status"
    >
      <Loader2 className="size-4 animate-spin" />
      {label}
    </div>
  );
}

export function EmptyState({
  title,
  description,
  action,
  className,
}: {
  title: string;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-2 px-6 py-12 text-center",
        className,
      )}
    >
      <Inbox className="text-subtle-foreground size-6" aria-hidden />
      <p className="text-foreground text-sm font-medium">{title}</p>
      {description ? (
        <p className="text-muted-foreground max-w-md text-[13px]">{description}</p>
      ) : null}
      {action}
    </div>
  );
}

/**
 * Failures always show the localized message and, when the request reached the
 * Server, the request id so it can be matched against audit events and logs.
 */
export function ErrorState({
  error,
  onRetry,
  className,
}: {
  error: unknown;
  onRetry?: () => void;
  className?: string;
}) {
  const forbidden = isForbidden(error);
  const requestId = errorRequestId(error);
  const Icon = forbidden ? ShieldAlert : AlertTriangle;

  return (
    <div
      className={cn("flex flex-col items-center gap-2 px-6 py-10 text-center", className)}
      role="alert"
    >
      <Icon className={cn("size-6", forbidden ? "text-warning" : "text-danger")} aria-hidden />
      <p className="text-foreground text-sm font-medium">
        {forbidden ? "没有访问权限" : "加载失败"}
      </p>
      <p className="text-muted-foreground max-w-md text-[13px]">{errorMessage(error)}</p>
      {requestId ? (
        <p className="zke-mono text-subtle-foreground text-xs">请求 ID：{requestId}</p>
      ) : null}
      {onRetry && !forbidden ? (
        <Button size="sm" variant="secondary" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </div>
  );
}
