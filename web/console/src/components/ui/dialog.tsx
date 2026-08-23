import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";

import { cn } from "@/lib/cn";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;

export function DialogContent({
  className,
  children,
  showClose = true,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Content> & { showClose?: boolean }) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="zke-overlay-motion fixed inset-0 z-1000 bg-black/35 backdrop-blur-[1px]" />
      <DialogPrimitive.Content
        className={cn(
          "zke-dialog-motion fixed top-1/2 left-1/2 z-1001 w-[min(520px,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2",
          // A dialog is portalled to the body, so it has no container to ask
          // about and is sized against the viewport instead. It declares one for
          // whatever is inside it: a form laid out in a dialog is inside a box
          // 358px wide on a phone, and that box is what it has to answer to.
          "rounded-window border-border bg-surface shadow-window-focused @container border p-4 sm:p-6",
          // `dvh`, not `vh`: on a phone `100vh` is the height the page would have
          // with the browser chrome retracted, so a dialog capped against it is
          // capped against space that is not on screen and loses its own footer
          // under the address bar.
          "max-h-[calc(100dvh-4rem)] overflow-y-auto",
          className,
        )}
        {...props}
      >
        {children}
        {showClose ? (
          <DialogPrimitive.Close
            className="zke-focus text-subtle-foreground hover:bg-surface-muted hover:text-foreground rounded-control absolute top-4 right-4 border border-transparent p-1 transition-colors"
            aria-label="关闭"
          >
            <X className="size-4" />
          </DialogPrimitive.Close>
        ) : null}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

export function DialogHeader({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("mb-4 flex flex-col gap-1.5 pr-8", className)} {...props} />;
}

export function DialogFooter({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("mt-5 flex flex-wrap justify-end gap-2", className)} {...props} />;
}

export function DialogTitle({
  className,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Title>) {
  return (
    <DialogPrimitive.Title
      className={cn("text-foreground text-[15px] font-semibold tracking-tight", className)}
      {...props}
    />
  );
}

export function DialogDescription({
  className,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      className={cn("text-muted-foreground text-[13px]", className)}
      {...props}
    />
  );
}
