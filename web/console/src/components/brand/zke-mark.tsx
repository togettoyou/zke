import { cn } from "@/lib/cn";

/**
 * The ZKE mark.
 *
 * A Z whose top bar runs unbroken and whose base is cut into three segments:
 * one plane to observe from, execution split across clusters. It is the
 * product's first principle drawn as a letter.
 *
 * The segments are a large-size detail, not a load-bearing one. Below roughly
 * 20px they fuse into a solid bar and the mark falls back to a plain Z — the
 * meaning is lost, the letter is not, which is the only failure mode a mark is
 * allowed at tab size.
 *
 * Built the way an application face is built — a tinted tile carrying a white
 * glyph, see `appFaceClass` — so the product's own identity speaks the same
 * language as the icons on its desktop, in `--app-blue`, and like every face it
 * is identical in both themes.
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
        "bg-linear-to-b from-[var(--app-blue-from)] to-[var(--app-blue-to)]",
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
        {/* The base. Three segments spanning exactly the top bar's rendered
            width, so the letter stays square however the eye measures it. */}
        <g fill="currentColor">
          <rect x="15.5" y="40.5" width="8.87" height="7" rx="2.6" />
          <rect x="27.57" y="40.5" width="8.87" height="7" rx="2.6" />
          <rect x="39.63" y="40.5" width="8.87" height="7" rx="2.6" />
        </g>
      </svg>
    </span>
  );
}
