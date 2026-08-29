import { cn } from "@/lib/cn";

/**
 * The ZKE mark.
 *
 * A Z whose top bar runs unbroken and whose base is cut into three segments:
 * one plane to observe from, execution split across clusters. It is the
 * product's first principle drawn as a letter.
 *
 * The split is a large-size detail, not a load-bearing one. Around 16px the
 * dots shrink to specks and the mark falls back to a plain Z — the meaning is
 * lost, the letter is not, which is the only failure mode a mark is allowed in
 * a tab strip.
 *
 * Built the way an application face is built — a tinted tile carrying a white
 * glyph, see `appFaceClass` — so the product's own identity speaks the same
 * language as the icons on its desktop, and like every face it is identical in
 * both themes. Its blue is its own token rather than an application's: the
 * desktop fills are keyed to the applications now, and the mark is not one.
 *
 * Decorative by design: every place the mark is drawn either names the product
 * beside it or sits in a bar that deliberately names nothing, so announcing a
 * second "ZKE" to a screen reader would only add noise.
 *
 * Callers supply size and radius; this supplies fill and ink.
 */
export function ZkeMark({ className }: { className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "block shrink-0 text-white",
        "bg-linear-to-b from-[var(--brand-mark-from)] to-[var(--brand-mark-to)]",
        className,
      )}
    >
      {/* Drawn on the tile's own 64-unit grid: stroke 7 and a 15.5-unit
          inset are what keep the letter open at 16px. */}
      <svg viewBox="0 0 64 64" className="block size-full" focusable="false">
        <path
          d="M19 20H45L19 44"
          fill="none"
          stroke="currentColor"
          strokeWidth="7"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
        {/* The base. Three units of the same 7 wide, evenly spaced across the
            top bar's rendered width (15.5 to 48.5), so the letter stays square
            however the eye measures it. The first unit is the diagonal's own
            round cap: a rounded rect drawn under it has squarer corners than
            the cap it overlaps, which ends the tail in a lumpy wedge rather
            than a foot. */}
        <g fill="currentColor">
          <circle cx="32" cy="44" r="3.5" />
          <circle cx="45" cy="44" r="3.5" />
        </g>
      </svg>
    </span>
  );
}
