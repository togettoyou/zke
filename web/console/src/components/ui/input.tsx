import * as React from "react";

import { cn } from "@/lib/cn";

const FIELD_BASE =
  "zke-focus zke-control border-border bg-surface text-foreground rounded-control w-full border text-sm shadow-e1 transition-[color,background-color,border-color,box-shadow] duration-150 placeholder:text-subtle-foreground hover:border-border-strong disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-danger";

export function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return <input type={type} className={cn(FIELD_BASE, "h-9 px-2.5", className)} {...props} />;
}

/**
 * Spread onto a credential field that is not the operator's own.
 *
 * A password manager decides a form was submitted partly by watching its fields
 * leave the DOM, and this Console never navigates: closing a window or a dialog
 * removes a whole subtree at once, which reads as exactly that. Left unmarked, a
 * field holding somebody else's password — an initial password being handed out,
 * a reset, a registry credential — makes the extension offer to save that
 * password into the operator's vault as if it were their login here.
 *
 * `autoComplete="off"` does not cover this: the major extensions ignore it by
 * design, because sites used it to break password managers on purpose. Each
 * vendor reads its own attribute instead, so all four are carried.
 */
export const CREDENTIAL_MANAGER_IGNORE = {
  "data-1p-ignore": "",
  "data-lpignore": "true",
  "data-bwignore": "",
  "data-form-type": "other",
} as const;

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      className={cn(FIELD_BASE, "min-h-20 px-2.5 py-2 leading-relaxed", className)}
      {...props}
    />
  );
}

/*
 * What a partially typed number looks like. `1.` and `.5` are on the way to a
 * number and must be allowed to exist while the caret is still in the field;
 * `1.2.3`, a sign or a letter never will be.
 */
const INTEGER_DRAFT = /^\d*$/;
const DECIMAL_DRAFT = /^\d*\.?\d*$/;

/**
 * An input that only ever holds a number.
 *
 * `inputMode` is a hint to the on-screen keyboard and nothing more — a field
 * carrying it still accepts pasted text, an IME and every letter on a physical
 * keyboard, which left forms validating at submit time what the field should
 * never have taken. A rejected keystroke leaves the value untouched rather than
 * being stripped out of it, so a pasted `abc` is refused whole instead of
 * landing as an empty field the operator did not ask for.
 */
export function NumericInput({
  value,
  onValueChange,
  decimal = false,
  className,
  ...props
}: Omit<React.ComponentProps<"input">, "value" | "onChange" | "inputMode" | "type"> & {
  value: string;
  onValueChange: (value: string) => void;
  /** Set for quantities that may carry a fraction, such as CPU cores. */
  decimal?: boolean;
}) {
  return (
    <Input
      {...props}
      value={value}
      inputMode={decimal ? "decimal" : "numeric"}
      autoComplete="off"
      className={cn("zke-tnum", className)}
      onChange={(event) => {
        const next = event.target.value;
        if ((decimal ? DECIMAL_DRAFT : INTEGER_DRAFT).test(next)) {
          onValueChange(next);
        }
      }}
    />
  );
}
