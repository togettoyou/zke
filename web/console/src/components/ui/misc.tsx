import * as React from "react";
import * as CheckboxPrimitive from "@radix-ui/react-checkbox";
import * as SeparatorPrimitive from "@radix-ui/react-separator";
import * as SwitchPrimitive from "@radix-ui/react-switch";
import { Check } from "lucide-react";

import { cn } from "@/lib/cn";

export function Separator({
  className,
  orientation = "horizontal",
  ...props
}: React.ComponentProps<typeof SeparatorPrimitive.Root>) {
  return (
    <SeparatorPrimitive.Root
      orientation={orientation}
      className={cn(
        "bg-border shrink-0",
        orientation === "horizontal" ? "h-px w-full" : "h-full w-px",
        className,
      )}
      {...props}
    />
  );
}

export function Checkbox({
  className,
  ...props
}: React.ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      className={cn(
        "zke-focus peer border-border-strong bg-surface size-4 shrink-0 rounded-[5px] border",
        "data-[state=checked]:border-primary data-[state=checked]:bg-primary",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator className="text-primary-foreground flex items-center justify-center">
        <Check className="size-3" strokeWidth={3} />
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  );
}

export function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        "bg-border-strong inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent transition-colors",
        "data-[state=checked]:bg-primary disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb className="bg-surface pointer-events-none block size-4 translate-x-0.5 rounded-full shadow-sm transition-transform data-[state=checked]:translate-x-4" />
    </SwitchPrimitive.Root>
  );
}

export function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("bg-surface-muted rounded-control animate-pulse", className)}
      aria-hidden
      {...props}
    />
  );
}

export function Card({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("border-border bg-surface rounded-panel shadow-e1 border p-4", className)}
      {...props}
    />
  );
}

export function CardTitle({ className, ...props }: React.ComponentProps<"h3">) {
  return (
    <h3
      className={cn("text-foreground text-[13px] font-semibold tracking-tight", className)}
      {...props}
    />
  );
}

type AlertTone = "info" | "warning" | "danger" | "success";

const ALERT_TONES: Record<AlertTone, string> = {
  info: "border-info/35 bg-info-surface text-info",
  warning: "border-warning/35 bg-warning-surface text-warning",
  danger: "border-danger/35 bg-danger-surface text-danger",
  success: "border-success/35 bg-success-surface text-success",
};

export function Alert({
  className,
  tone = "info",
  ...props
}: React.ComponentProps<"div"> & { tone?: AlertTone }) {
  return (
    <div
      role="status"
      className={cn(
        "rounded-control border px-3 py-2 text-[13px] leading-relaxed",
        ALERT_TONES[tone],
        className,
      )}
      {...props}
    />
  );
}
