import * as React from "react";
import * as SelectPrimitive from "@radix-ui/react-select";
import { Check, ChevronDown } from "lucide-react";

import { cn } from "@/lib/cn";

export const Select = SelectPrimitive.Root;
export const SelectValue = SelectPrimitive.Value;
export const SelectGroup = SelectPrimitive.Group;

export function SelectTrigger({
  className,
  children,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Trigger>) {
  return (
    <SelectPrimitive.Trigger
      className={cn(
        "zke-focus border-border bg-surface text-foreground rounded-control shadow-e1 flex h-9 w-full items-center justify-between gap-2 border px-2.5 text-sm transition-[color,background-color,border-color,box-shadow] duration-150",
        "data-[placeholder]:text-subtle-foreground hover:border-border-strong",
        "disabled:cursor-not-allowed disabled:opacity-60",
        className,
      )}
      {...props}
    >
      {/*
       * The value is a flex item, and a flex item's default `min-width: auto`
       * refuses to shrink below its content. A long option — an audit action
       * name, a Namespace — would then run out of a trigger whose width the
       * caller fixed, and push the chevron out with it. Shrinking is allowed
       * here, and what does not fit ends in an ellipsis.
       */}
      <span className="min-w-0 flex-1 truncate text-left">{children}</span>
      <SelectPrimitive.Icon asChild>
        <ChevronDown className="text-muted-foreground size-4 shrink-0" />
      </SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
  );
}

export function SelectContent({
  className,
  children,
  position = "popper",
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Content>) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        position={position}
        className={cn(
          "zke-pop-motion border-border bg-surface shadow-e3 rounded-panel z-1100 max-h-72 min-w-[var(--radix-select-trigger-width)] overflow-hidden border",
          className,
        )}
        {...props}
      >
        <SelectPrimitive.Viewport className="p-1">{children}</SelectPrimitive.Viewport>
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  );
}

export function SelectItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Item>) {
  return (
    <SelectPrimitive.Item
      className={cn(
        "text-foreground rounded-control relative flex cursor-default items-center py-1.5 pr-8 pl-2 text-[13px] outline-none select-none",
        "data-[highlighted]:bg-surface-muted data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        className,
      )}
      {...props}
    >
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
      <span className="absolute right-2 flex size-3.5 items-center justify-center">
        <SelectPrimitive.ItemIndicator>
          <Check className="text-primary size-3.5" />
        </SelectPrimitive.ItemIndicator>
      </span>
    </SelectPrimitive.Item>
  );
}

export function SelectLabel({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Label>) {
  return (
    <SelectPrimitive.Label
      className={cn("text-subtle-foreground px-2 py-1.5 text-xs", className)}
      {...props}
    />
  );
}
