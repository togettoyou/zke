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
        "zke-focus text-muted-foreground rounded-control border border-transparent px-3 py-1 text-[13px] font-medium transition-colors",
        "hover:text-foreground",
        "data-[state=active]:border-border data-[state=active]:bg-surface data-[state=active]:text-foreground data-[state=active]:shadow-e1",
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
