import * as React from "react";
import * as PopoverPrimitive from "@radix-ui/react-popover";

import { cn } from "@/lib/cn";

export const Popover = PopoverPrimitive.Root;
export const PopoverTrigger = PopoverPrimitive.Trigger;
export const PopoverAnchor = PopoverPrimitive.Anchor;

export function PopoverContent({
  className,
  align = "center",
  sideOffset = 8,
  ...props
}: React.ComponentProps<typeof PopoverPrimitive.Content>) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        align={align}
        sideOffset={sideOffset}
        className={cn(
          // Radix measures the room it actually has and publishes it; without
          // reading it back a `w-80` popover is 320px wide on a 390px phone and
          // simply hangs off the edge it was flipped away from.
          "zke-pop-motion border-border bg-surface shadow-e3 rounded-panel z-1100 w-72 max-w-[var(--radix-popover-content-available-width)] border p-3 outline-none",
          className,
        )}
        {...props}
      />
    </PopoverPrimitive.Portal>
  );
}
