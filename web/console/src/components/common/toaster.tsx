import type { CSSProperties } from "react";
import { Toaster as SonnerToaster } from "sonner";

import { useThemeStore } from "@/theme/theme-store";

/**
 * The Console's toast surface.
 *
 * Sonner ships its own palette and defaults to the light one regardless of what
 * the page is wearing, which is how every notification in the product used to
 * arrive as a white card on a dark desktop. Nothing here is a new design: the
 * toast is given the same surface, hairline, radius and elevation as every other
 * panel in the Console.
 *
 * The fill, ink and border go through Sonner's own custom properties rather than
 * through classes. Sonner's stylesheet is unlayered, so it outranks anything
 * Tailwind emits inside `@layer utilities` no matter how specific — overriding
 * the variables it already reads is the one way to restyle it that does not turn
 * into a wall of `!important`. What is left to classes is the shape and the
 * type, where there is nothing to outrank.
 *
 * `richColors` is deliberately off. It swaps in Sonner's reds and greens, which
 * are not this product's `--danger` and `--success`; a failure toast in one hue
 * beside a failure alert in another is the sort of thing an operator reads as
 * two different severities. Tone is carried instead by the icon and the title,
 * in the Console's own semantic ink, over the ordinary surface.
 */
export function AppToaster() {
  const theme = useThemeStore((state) => state.theme);

  return (
    <SonnerToaster
      position="bottom-right"
      theme={theme}
      closeButton
      style={
        {
          // Below dialogs and menus. A toast is a report, never a thing to be
          // answered, so it must not land on top of the surface asking a
          // question.
          zIndex: 900,
          "--normal-bg": "var(--surface)",
          "--normal-text": "var(--foreground)",
          "--normal-border": "var(--border)",
          "--border-radius": "var(--radius-panel)",
        } as CSSProperties
      }
      /*
       * The one place in the Console that reaches for `!`. Everything below
       * overrides a declaration Sonner already makes on the same element from an
       * unlayered stylesheet, and layer order — not specificity — is what decides
       * that contest, so nothing short of `!important` reaches it.
       */
      toastOptions={{
        classNames: {
          toast: "shadow-e3! font-sans!",
          title: "text-[13px]! font-medium!",
          description: "text-muted-foreground! text-xs! leading-relaxed!",
          error: "[&_[data-icon]]:text-danger! [&_[data-title]]:text-danger!",
          success: "[&_[data-icon]]:text-success! [&_[data-title]]:text-success!",
          warning: "[&_[data-icon]]:text-warning! [&_[data-title]]:text-warning!",
          info: "[&_[data-icon]]:text-info! [&_[data-title]]:text-info!",
        },
      }}
    />
  );
}
