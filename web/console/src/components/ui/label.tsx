import * as React from "react";
import * as LabelPrimitive from "@radix-ui/react-label";

import { cn } from "@/lib/cn";

export function Label({ className, ...props }: React.ComponentProps<typeof LabelPrimitive.Root>) {
  return (
    <LabelPrimitive.Root
      className={cn(
        "text-foreground text-[13px] leading-none font-medium select-none",
        "peer-disabled:cursor-not-allowed peer-disabled:opacity-60",
        className,
      )}
      {...props}
    />
  );
}

export function FieldHint({ className, ...props }: React.ComponentProps<"p">) {
  return <p className={cn("text-muted-foreground text-xs", className)} {...props} />;
}

export function FieldError({ className, ...props }: React.ComponentProps<"p">) {
  return <p className={cn("text-danger text-xs", className)} role="alert" {...props} />;
}
