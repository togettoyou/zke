import * as React from "react";

import { cn } from "@/lib/cn";

const FIELD_BASE =
  "zke-focus border-border bg-surface text-foreground rounded-control w-full border text-sm shadow-e1 transition-[color,background-color,border-color,box-shadow] duration-150 placeholder:text-subtle-foreground hover:border-border-strong disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-danger";

export function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return <input type={type} className={cn(FIELD_BASE, "h-9 px-2.5", className)} {...props} />;
}

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      className={cn(FIELD_BASE, "min-h-20 px-2.5 py-2 leading-relaxed", className)}
      {...props}
    />
  );
}
