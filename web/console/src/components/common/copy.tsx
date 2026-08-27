import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { HintTooltip } from "@/components/ui/tooltip";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";

/**
 * Copying, and the two seconds afterwards that say it worked.
 *
 * The confirmation is state rather than a toast: an answer, a tool result and a
 * code block all offer this, and a toast for each one would report on the
 * clipboard more loudly than on the cluster. A failure is different — the
 * browser can still refuse the write, and a control that silently does nothing
 * is the worst of both.
 */
function useCopy(): { copied: boolean; copy: (value: string | (() => string)) => void } {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  const alive = useRef(true);

  useEffect(
    () => () => {
      alive.current = false;
      window.clearTimeout(timer.current);
    },
    [],
  );

  const copy = useCallback((value: string | (() => string)) => {
    // A function so a caller whose text is expensive to materialise — a whole
    // tool result, a rendered trajectory entry — does not build it every render.
    const text = typeof value === "function" ? value() : value;
    void copyText(text).then(
      () => {
        if (!alive.current) return;
        setCopied(true);
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => {
          if (alive.current) setCopied(false);
        }, 1_600);
      },
      () => {
        // Not `notifyFailure`: nothing here ever reached the Server, and its
        // message for a non-API error is "网络错误", which points the operator
        // at the wrong thing entirely.
        if (alive.current) {
          toast.error("复制失败", { description: "浏览器不允许写入剪贴板，请手动选择文本复制。" });
        }
      },
    );
  }, []);

  return { copied, copy };
}

/**
 * The copy control as it appears in a row of message actions.
 *
 * Icon-only and ghost, because it sits under an answer rather than in a
 * toolbar: the answer is what is being read, and a filled button under every
 * one of them would draw the eye down the column instead of across the text.
 */
export function CopyIconButton({
  value,
  label = "复制",
  className,
}: {
  value: string | (() => string);
  label?: string;
  className?: string;
}) {
  const { copied, copy } = useCopy();
  return (
    <HintTooltip label={copied ? "已复制" : label}>
      <Button
        type="button"
        size="icon-sm"
        variant="ghost"
        aria-label={copied ? "已复制" : label}
        className={cn(copied && "text-success", className)}
        onClick={(event) => {
          // These sit inside rows that select something when clicked.
          event.stopPropagation();
          copy(value);
        }}
      >
        {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
      </Button>
    </HintTooltip>
  );
}
