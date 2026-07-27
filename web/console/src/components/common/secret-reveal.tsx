import { useState } from "react";
import { Eye, EyeOff, ShieldAlert } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { cn } from "@/lib/cn";

import { CopyButton } from "./status";

/**
 * One-time display of a credential (enrollment token, install command).
 *
 * The value is masked until explicitly revealed, is never written to storage,
 * the URL or logs, and the surrounding copy states that it cannot be retrieved
 * again.
 */
export function SecretReveal({
  value,
  label,
  hint,
  className,
}: {
  value: string;
  label: string;
  hint?: string;
  className?: string;
}) {
  const [revealed, setRevealed] = useState(false);

  return (
    <div className={cn("grid gap-2", className)}>
      <div className="flex items-center justify-between gap-2">
        <span className="text-foreground text-[13px] font-medium">{label}</span>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="secondary"
            onClick={() => setRevealed((current) => !current)}
            aria-pressed={revealed}
          >
            {revealed ? <EyeOff /> : <Eye />}
            {revealed ? "隐藏" : "显示"}
          </Button>
          <CopyButton value={value} />
        </div>
      </div>

      <pre
        className={cn(
          "zke-mono border-border bg-surface-muted rounded-control max-h-40 overflow-auto border p-3 text-xs break-all whitespace-pre-wrap",
          !revealed && "text-transparent select-none [text-shadow:0_0_8px_var(--muted-foreground)]",
        )}
        aria-label={revealed ? label : `${label}（已遮蔽）`}
      >
        {value}
      </pre>

      <Alert tone="warning" className="flex items-start gap-2">
        <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
        <span>
          该凭证只显示一次，关闭后无法再次获取。请通过安全渠道传递，不要写入聊天记录、工单或代码仓库。
          {hint ? ` ${hint}` : ""}
        </span>
      </Alert>
    </div>
  );
}
