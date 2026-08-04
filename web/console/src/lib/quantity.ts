/**
 * Kubernetes resource quantities, in the two directions a unit-labelled form
 * needs them.
 *
 * A quota is written by Kubernetes in whatever unit the author chose — `2`,
 * `1500m`, `4Gi`, `1000Mi` all name real amounts — while a form that puts 核 or
 * Gi next to the input has to show one number in one unit. Reading is therefore
 * lossy at the margins (1000Mi is not a whole number of Gi), which is why the
 * caller is expected to keep the original string and resubmit it unchanged for
 * any field the operator did not touch.
 */

/** The unit a form field displays a quantity in. */
export type QuantityUnit = "cores" | "gib" | "count";

const GIB = 1024 ** 3;

const SUFFIX_FACTORS: Record<string, number> = {
  n: 1e-9,
  u: 1e-6,
  m: 1e-3,
  k: 1e3,
  M: 1e6,
  G: 1e9,
  T: 1e12,
  P: 1e15,
  E: 1e18,
  Ki: 1024,
  Mi: 1024 ** 2,
  Gi: 1024 ** 3,
  Ti: 1024 ** 4,
  Pi: 1024 ** 5,
  Ei: 1024 ** 6,
};

/*
 * `<signedNumber><suffix>`, where the suffix is a binary unit, a decimal unit or
 * a decimal exponent — Kubernetes allows one of the three, never two. The
 * alternation puts the two-character binary units first so `Mi` is not read as
 * `M` with a stray `i`, and leaves lowercase `e` out of the single-character
 * class so `1e5` can only match the exponent branch while a bare `E` still means
 * exa.
 */
const QUANTITY = /^([+-]?(?:\d+\.?\d*|\.\d+))(Ki|Mi|Gi|Ti|Pi|Ei|[numkMGTPE]|[eE][+-]?\d+)?$/;

/** A plain non-negative decimal, which is all a unit-labelled field accepts. */
const DECIMAL = /^\d+\.?\d*$|^\.\d+$/;

/**
 * Reads a Kubernetes quantity into a plain number of base units: cores for CPU,
 * bytes for memory and storage, objects for counts. Returns null for anything
 * Kubernetes would not have written.
 */
export function parseQuantity(value: string): number | null {
  const match = QUANTITY.exec(value.trim());
  if (!match) {
    return null;
  }
  const amount = Number(match[1]);
  if (!Number.isFinite(amount)) {
    return null;
  }
  const suffix = match[2];
  if (!suffix) {
    return amount;
  }
  if (suffix[0] === "e" || suffix[0] === "E") {
    const exponent = Number(suffix.slice(1));
    // A bare `E` is exa and has no digits after it, so it is not an exponent.
    if (Number.isFinite(exponent) && suffix.length > 1) {
      return amount * 10 ** exponent;
    }
  }
  const factor = SUFFIX_FACTORS[suffix];
  return factor === undefined ? null : amount * factor;
}

/**
 * Formats a Kubernetes quantity as the number a field labelled with `unit`
 * shows. Returns null when the value is not a quantity at all, so the caller can
 * fall back to displaying it verbatim rather than inventing a number.
 */
export function quantityToDisplay(value: string, unit: QuantityUnit): string | null {
  const base = parseQuantity(value);
  if (base === null || base < 0) {
    return null;
  }
  switch (unit) {
    case "cores":
      // Kubernetes resolves CPU no finer than 1m, so three decimals is the whole
      // range rather than a rounding choice.
      return trimNumber(base, 3);
    case "gib":
      return trimNumber(base / GIB, 6);
    case "count":
      return Number.isInteger(base) ? String(base) : trimNumber(base, 3);
  }
}

/**
 * Turns what the operator typed into the quantity string to submit, or null when
 * the field does not hold a number this unit can carry.
 *
 * The number is passed through rather than normalised: `0.5` and `4` are both
 * quantities Kubernetes accepts, and it canonicalises them itself. Rewriting
 * them here would only add a second opinion about how the same amount is spelled.
 */
export function displayToQuantity(display: string, unit: QuantityUnit): string | null {
  const trimmed = display.trim();
  if (!DECIMAL.test(trimmed)) {
    return null;
  }
  const amount = Number(trimmed);
  if (!Number.isFinite(amount) || amount < 0) {
    return null;
  }
  switch (unit) {
    case "cores":
      return trimmed;
    case "gib":
      return `${trimmed}Gi`;
    case "count":
      return Number.isInteger(amount) ? String(amount) : null;
  }
}

/** Fixed-point with the trailing zeros removed, so 4.000 reads as 4. */
function trimNumber(value: number, decimals: number): string {
  const fixed = value.toFixed(decimals);
  return fixed.includes(".") ? fixed.replace(/\.?0+$/, "") : fixed;
}
