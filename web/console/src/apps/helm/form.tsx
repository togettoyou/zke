import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Package } from "lucide-react";

import { FieldHint, Label } from "@/components/ui/label";
import { Alert, Checkbox } from "@/components/ui/misc";
import { cn } from "@/lib/cn";

/**
 * The pieces every Helm form is built from.
 *
 * They exist so the layout is decided once. A form whose fields each carry their
 * own spacing drifts the moment one of them grows a hint line: the control next
 * to it stays where it was, and the two stop lining up. Here a field is always
 * label, control, then optional hint, and the grid it sits in aligns rows by
 * their tops rather than stretching them.
 */

/** One labelled control. `content-start` is what keeps it from stretching to fill its grid row. */
export function Field({
  label,
  htmlFor,
  hint,
  error,
  className,
  children,
}: {
  label: string;
  htmlFor?: string;
  hint?: ReactNode;
  error?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("grid content-start gap-1.5", className)}>
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {error ? (
        <p className="text-danger text-xs" role="alert">
          {error}
        </p>
      ) : hint ? (
        <FieldHint>{hint}</FieldHint>
      ) : null}
    </div>
  );
}

/**
 * A row of fields.
 *
 * Two columns at `@md` and no more: these forms have long labels and long hints,
 * and a third column inside a 1060px window leaves each field too narrow to read
 * the hint under it.
 */
export function FieldGrid({ children }: { children: ReactNode }) {
  return <div className="grid items-start gap-x-4 gap-y-3 @md:grid-cols-2">{children}</div>;
}

export function FormSection({
  title,
  hint,
  warning,
  children,
}: {
  title: string;
  hint?: ReactNode;
  /** A condition of this section the operator has to know before filling it in. */
  warning?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="border-border bg-surface rounded-panel border p-4">
      <div className="mb-3 flex flex-wrap items-baseline gap-2">
        <h4 className="text-foreground text-[13px] font-semibold tracking-tight">{title}</h4>
        {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
      </div>
      {warning ? (
        <Alert tone="warning" className="mb-3">
          {warning}
        </Alert>
      ) : null}
      {children}
    </section>
  );
}

/**
 * A switch with its consequence written under it.
 *
 * The checkbox is nudged down by a hair rather than centred: it belongs to the
 * label's first line, and centring it against a two-line block leaves it
 * floating between the label and the hint.
 */
export function SwitchField({
  id,
  checked,
  onChange,
  label,
  hint,
  tone,
  disabled,
}: {
  id: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  hint: ReactNode;
  tone?: "warning";
  disabled?: boolean;
}) {
  return (
    <div className="flex items-start gap-2.5">
      <Checkbox
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={(value) => onChange(value === true)}
        className="mt-0.5"
      />
      <div className="grid gap-1">
        <Label htmlFor={id} className={tone === "warning" ? "text-warning" : undefined}>
          {label}
        </Label>
        <FieldHint>{hint}</FieldHint>
      </div>
    </div>
  );
}

/**
 * A chart's own icon.
 *
 * The URL comes from the repository index, which is to say from whoever
 * published the chart. It is rendered as an image and nothing else: no chart
 * decides layout, and a scheme other than http, https or an inline image is not
 * loaded at all — `javascript:` in an `<img src>` does nothing in a modern
 * browser, but a URL from outside ZKE should not be handed to the browser on the
 * assumption that it will keep being harmless.
 *
 * A missing or broken icon falls back to a glyph rather than to a gap. Most
 * charts have no icon, so the fallback is the common case and has to look
 * deliberate.
 */
export function ChartIcon({
  url,
  size = "md",
  icon: Fallback = Package,
}: {
  url?: string;
  size?: "sm" | "md" | "lg";
  icon?: LucideIcon;
}) {
  const box = size === "lg" ? "size-12" : size === "sm" ? "size-6" : "size-9";
  const glyph = size === "lg" ? "size-6" : size === "sm" ? "size-3.5" : "size-4.5";
  return (
    <span
      className={cn(
        "border-border bg-surface-muted rounded-control flex shrink-0 items-center justify-center overflow-hidden border",
        box,
      )}
    >
      {safeIconURL(url) ? (
        <img
          src={url}
          alt=""
          loading="lazy"
          // The repository learns nothing about who is browsing it from ZKE.
          referrerPolicy="no-referrer"
          className="size-full object-contain p-1"
          onError={(event) => {
            // Replaced rather than hidden: an empty tile is the same shape as a
            // chart that simply has no icon, which is what it now is.
            event.currentTarget.remove();
          }}
        />
      ) : (
        <Fallback className={cn("text-subtle-foreground", glyph)} aria-hidden="true" />
      )}
    </span>
  );
}

function safeIconURL(url?: string): boolean {
  if (!url) {
    return false;
  }
  const lower = url.trim().toLowerCase();
  return (
    lower.startsWith("https://") || lower.startsWith("http://") || lower.startsWith("data:image/")
  );
}
