export type WindowRect = { x: number; y: number; width: number; height: number };

export type DesktopBounds = { x: number; y: number; width: number; height: number };

export type Viewport = { width: number; height: number };

export const TOP_BAR_HEIGHT = 44;
export const DOCK_RESERVED_HEIGHT = 84;
export const DESKTOP_MARGIN = 12;

export const MINIMUM_WINDOW_WIDTH = 380;
export const MINIMUM_WINDOW_HEIGHT = 260;

/**
 * How close to a screen edge a drag must come before it snaps.
 *
 * Deliberately tight. The top bar sits behind windows, so dropping one on top
 * of it is a normal thing to want; snapping must therefore require pushing the
 * pointer against the screen edge itself, not merely reaching the bar.
 */
export const SNAP_EDGE_THRESHOLD = 4;

/** Keep at least this much of the title bar reachable by the pointer. */
const MINIMUM_VISIBLE_WIDTH = 120;
const TITLE_BAR_HEIGHT = 38;

/**
 * Where new windows are placed: inside the top bar, the Dock and a margin.
 *
 * This is a placement preference, not a limit. Windows may be dragged, resized
 * and snapped beyond it — {@link clampRect} constrains them to the screen.
 */
export function computeDesktopBounds(viewportWidth: number, viewportHeight: number): DesktopBounds {
  return {
    x: DESKTOP_MARGIN,
    y: TOP_BAR_HEIGHT + DESKTOP_MARGIN,
    width: Math.max(MINIMUM_WINDOW_WIDTH, viewportWidth - DESKTOP_MARGIN * 2),
    height: Math.max(
      MINIMUM_WINDOW_HEIGHT,
      viewportHeight - TOP_BAR_HEIGHT - DOCK_RESERVED_HEIGHT - DESKTOP_MARGIN,
    ),
  };
}

/**
 * A maximized window covers the whole viewport, including the strips the top
 * bar and the Dock normally occupy — both step out of its way.
 */
export function fullscreenRect(viewport: Viewport): WindowRect {
  return { x: 0, y: 0, width: viewport.width, height: viewport.height };
}

/**
 * Constrains a window to the screen.
 *
 * A window may cover the top bar and the Dock, so the limit is the viewport
 * rather than the placement bounds. What it may not do is put its title bar out
 * of reach: never above the top edge, never below the bottom edge, and never
 * fully off either side.
 */
export function clampRect(rect: WindowRect, viewport: Viewport): WindowRect {
  const width = Math.max(MINIMUM_WINDOW_WIDTH, Math.min(rect.width, viewport.width));
  const height = Math.max(MINIMUM_WINDOW_HEIGHT, Math.min(rect.height, viewport.height));
  const minimumX = MINIMUM_VISIBLE_WIDTH - width;
  const maximumX = viewport.width - MINIMUM_VISIBLE_WIDTH;
  const maximumY = viewport.height - TITLE_BAR_HEIGHT;
  return {
    width,
    height,
    x: Math.round(Math.min(Math.max(rect.x, minimumX), maximumX)),
    y: Math.round(Math.min(Math.max(rect.y, 0), maximumY)),
  };
}

/** Cascades new windows so a second instance never hides the first. */
export function cascadeRect(
  bounds: DesktopBounds,
  index: number,
  size: { width: number; height: number },
  viewport: Viewport,
): WindowRect {
  const step = 26;
  const offset = (index % 6) * step;
  const width = Math.min(size.width, bounds.width);
  const height = Math.min(size.height, bounds.height);
  return clampRect(
    {
      x: bounds.x + offset + Math.max(0, Math.round((bounds.width - width) / 6)),
      y: bounds.y + offset,
      width,
      height,
    },
    viewport,
  );
}

export type SnapZone = "maximize" | "left" | "right" | null;

/** Edge snapping: the top edge maximizes, left and right take half the screen. */
export function detectSnapZone(pointerX: number, pointerY: number, viewport: Viewport): SnapZone {
  if (pointerY <= SNAP_EDGE_THRESHOLD) {
    return "maximize";
  }
  if (pointerX <= SNAP_EDGE_THRESHOLD) {
    return "left";
  }
  if (pointerX >= viewport.width - SNAP_EDGE_THRESHOLD) {
    return "right";
  }
  return null;
}

/** Half screen means half the screen: full height, flush against its edge. */
export function snapRect(zone: Exclude<SnapZone, null>, viewport: Viewport): WindowRect {
  if (zone === "maximize") {
    return fullscreenRect(viewport);
  }
  const width = Math.round(viewport.width / 2);
  return {
    x: zone === "left" ? 0 : viewport.width - width,
    y: 0,
    width,
    height: viewport.height,
  };
}

export type ResizeEdge = "n" | "s" | "e" | "w" | "ne" | "nw" | "se" | "sw";

/** Applies a pointer delta to one edge or corner, honouring minimum sizes. */
export function resizeRect(
  rect: WindowRect,
  edge: ResizeEdge,
  deltaX: number,
  deltaY: number,
  viewport: Viewport,
): WindowRect {
  let { x, y, width, height } = rect;

  if (edge.includes("e")) {
    width = rect.width + deltaX;
  }
  if (edge.includes("s")) {
    height = rect.height + deltaY;
  }
  if (edge.includes("w")) {
    width = rect.width - deltaX;
    x = rect.x + deltaX;
  }
  if (edge.includes("n")) {
    height = rect.height - deltaY;
    y = rect.y + deltaY;
  }

  if (width < MINIMUM_WINDOW_WIDTH) {
    if (edge.includes("w")) {
      x -= MINIMUM_WINDOW_WIDTH - width;
    }
    width = MINIMUM_WINDOW_WIDTH;
  }
  if (height < MINIMUM_WINDOW_HEIGHT) {
    if (edge.includes("n")) {
      y -= MINIMUM_WINDOW_HEIGHT - height;
    }
    height = MINIMUM_WINDOW_HEIGHT;
  }

  return clampRect({ x, y, width, height }, viewport);
}
