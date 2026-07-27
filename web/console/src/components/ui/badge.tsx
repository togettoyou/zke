import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/cn";

const badgeVariants = cva(
  // A hairline in the tone's own colour keeps the pill legible on tinted rows,
  // where a fill alone loses its edge.
  //
  // `w-fit` is load-bearing: badges are routinely dropped into `flex flex-col`
  // table cells, whose default `align-items: stretch` would otherwise pull the
  // pill across the whole column. `inline-flex` alone does not prevent that.
  "inline-flex w-fit items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap",
  {
    variants: {
      tone: {
        neutral: "border-neutral/20 bg-neutral-surface text-neutral",
        success: "border-success/25 bg-success-surface text-success",
        warning: "border-warning/25 bg-warning-surface text-warning",
        danger: "border-danger/25 bg-danger-surface text-danger",
        info: "border-info/25 bg-info-surface text-info",
        primary: "border-primary/25 bg-primary-surface text-primary",
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
  return <span aria-hidden className={cn("size-[5px] shrink-0 rounded-full", color)} />;
}
