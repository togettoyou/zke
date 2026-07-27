import * as React from "react";

import { cn } from "@/lib/cn";

export function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      className={cn(
        "border-border bg-surface text-foreground h-9 w-full rounded-md border px-3 text-sm transition-colors",
        "placeholder:text-subtle-foreground",
        "focus-visible:border-primary",
        "disabled:cursor-not-allowed disabled:opacity-60",
        "aria-invalid:border-danger",
        className,
      )}
      {...props}
    />
  );
}

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      className={cn(
        "border-border bg-surface text-foreground min-h-20 w-full rounded-md border px-3 py-2 text-sm",
        "placeholder:text-subtle-foreground focus-visible:border-primary",
        className,
      )}
      {...props}
    />
  );
}
