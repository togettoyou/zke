import * as React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";

import { cn } from "@/lib/cn";

export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn(
        "border-border bg-surface-muted rounded-panel inline-flex items-center gap-1 border p-1",
        className,
      )}
      {...props}
    />
  );
}

export function TabsTrigger({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        // inline-flex rather than the default inline-block: a trigger that
        // carries an icon beside its label needs the two on one line, and a
        // `gap-*` on an inline-block box does nothing at all — the icon wraps
        // above the text instead, which is how it looked before.
        "zke-focus text-muted-foreground rounded-control inline-flex items-center gap-1.5 border border-transparent px-3 py-1 text-[13px] font-medium whitespace-nowrap transition-colors",
        "hover:text-foreground",
        "data-[state=active]:border-border data-[state=active]:bg-surface data-[state=active]:text-foreground data-[state=active]:shadow-e1",
        // A tab that cannot be opened has to look like it: without this it
        // stays fully legible and simply ignores the click, which reads as a
        // broken control rather than an unavailable one.
        "disabled:text-subtle-foreground disabled:pointer-events-none disabled:opacity-55",
        className,
      )}
      {...props}
    />
  );
}

export function TabsContent({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content className={cn("mt-3 outline-none", className)} {...props} />;
}
