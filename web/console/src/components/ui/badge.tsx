import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/cn";

const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap",
  {
    variants: {
      tone: {
        neutral: "bg-neutral-surface text-neutral",
        success: "bg-success-surface text-success",
        warning: "bg-warning-surface text-warning",
        danger: "bg-danger-surface text-danger",
        info: "bg-info-surface text-info",
        primary: "bg-primary-surface text-primary",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);

export type BadgeProps = React.ComponentProps<"span"> & VariantProps<typeof badgeVariants>;

export function Badge({ className, tone, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ tone }), className)} {...props} />;
}

/**
 * Status is never communicated by color alone: the dot is paired with the
 * textual label supplied as children.
 */
export function StatusDot({ tone = "neutral" }: { tone?: BadgeProps["tone"] }) {
  const color = {
    neutral: "bg-neutral",
    success: "bg-success",
    warning: "bg-warning",
    danger: "bg-danger",
    info: "bg-info",
    primary: "bg-primary",
  }[tone ?? "neutral"];
  return <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", color)} />;
}
