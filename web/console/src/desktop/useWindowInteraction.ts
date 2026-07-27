import { useCallback, useEffect, useRef, useState } from "react";

import {
  clampRect,
  detectSnapZone,
  resizeRect,
  snapRect,
  type ResizeEdge,
  type SnapZone,
  type Viewport,
  type WindowRect,
} from "./geometry";

type InteractionKind = { type: "drag" } | { type: "resize"; edge: ResizeEdge };

type Options = {
  rect: WindowRect;
  /** Windows move, resize and snap against the screen, not the placement area. */
  viewport: Viewport;
  disabled?: boolean;
  onStart: () => void;
  onCommit: (rect: WindowRect) => void;
  /** Called when a drag ends inside a snap zone. */
  onSnap: (zone: Exclude<SnapZone, null>) => void;
};

type PointerState = {
  pointerId: number;
  startX: number;
  startY: number;
  startRect: WindowRect;
  kind: InteractionKind;
  element: Element;
};

/**
 * Pointer-driven window move and resize.
 *
 * Geometry updates run through requestAnimationFrame and are kept local until
 * the pointer is released, so dragging never re-renders the rest of the desktop
 * and the store only sees the final rectangle. Escape cancels an interaction in
 * progress and restores the original geometry.
 */
export function useWindowInteraction({
  rect,
  viewport,
  disabled = false,
  onStart,
  onCommit,
  onSnap,
}: Options) {
  const [previewRect, setPreviewRect] = useState<WindowRect | null>(null);
  const [snapZone, setSnapZone] = useState<SnapZone>(null);
  const pointerState = useRef<PointerState | null>(null);
  const frame = useRef<number | null>(null);
  const latestRect = useRef<WindowRect>(rect);
  const latestSnap = useRef<SnapZone>(null);

  const finishInteraction = useCallback(() => {
    if (frame.current !== null) {
      cancelAnimationFrame(frame.current);
      frame.current = null;
    }
    const state = pointerState.current;
    if (state?.element.hasPointerCapture?.(state.pointerId)) {
      state.element.releasePointerCapture(state.pointerId);
    }
    pointerState.current = null;
    document.body.classList.remove("zke-interacting");
    setPreviewRect(null);
    setSnapZone(null);
  }, []);

  const cancelInteraction = useCallback(() => {
    latestRect.current = rect;
    latestSnap.current = null;
    finishInteraction();
  }, [finishInteraction, rect]);

  useEffect(() => {
    if (!previewRect) {
      return;
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        cancelInteraction();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [previewRect, cancelInteraction]);

  useEffect(() => finishInteraction, [finishInteraction]);

  const beginInteraction = useCallback(
    (event: React.PointerEvent<HTMLElement>, kind: InteractionKind) => {
      if (disabled || event.button !== 0) {
        return;
      }
      event.preventDefault();
      onStart();
      const element = event.currentTarget;
      element.setPointerCapture?.(event.pointerId);
      pointerState.current = {
        pointerId: event.pointerId,
        startX: event.clientX,
        startY: event.clientY,
        startRect: rect,
        kind,
        element,
      };
      latestRect.current = rect;
      latestSnap.current = null;
      document.body.classList.add("zke-interacting");
      setPreviewRect(rect);
    },
    [disabled, onStart, rect],
  );

  const handlePointerMove = useCallback(
    (event: React.PointerEvent<HTMLElement>) => {
      const state = pointerState.current;
      if (!state || state.pointerId !== event.pointerId) {
        return;
      }
      const deltaX = event.clientX - state.startX;
      const deltaY = event.clientY - state.startY;
      const pointerX = event.clientX;
      const pointerY = event.clientY;

      if (frame.current !== null) {
        return;
      }
      frame.current = requestAnimationFrame(() => {
        frame.current = null;
        if (state.kind.type === "drag") {
          const moved = clampRect(
            {
              ...state.startRect,
              x: state.startRect.x + deltaX,
              y: state.startRect.y + deltaY,
            },
            viewport,
          );
          const zone = detectSnapZone(pointerX, pointerY, viewport);
          latestRect.current = moved;
          latestSnap.current = zone;
          setPreviewRect(moved);
          setSnapZone(zone);
          return;
        }
        const resized = resizeRect(state.startRect, state.kind.edge, deltaX, deltaY, viewport);
        latestRect.current = resized;
        setPreviewRect(resized);
      });
    },
    [viewport],
  );

  const handlePointerUp = useCallback(
    (event: React.PointerEvent<HTMLElement>) => {
      const state = pointerState.current;
      if (!state || state.pointerId !== event.pointerId) {
        return;
      }
      const zone = latestSnap.current;
      const finalRect = latestRect.current;
      finishInteraction();
      if (zone) {
        onSnap(zone);
        return;
      }
      onCommit(finalRect);
    },
    [finishInteraction, onCommit, onSnap],
  );

  const dragHandleProps = {
    onPointerDown: (event: React.PointerEvent<HTMLElement>) =>
      beginInteraction(event, { type: "drag" }),
    onPointerMove: handlePointerMove,
    onPointerUp: handlePointerUp,
    onPointerCancel: cancelInteraction,
  };

  const resizeHandleProps = (edge: ResizeEdge) => ({
    onPointerDown: (event: React.PointerEvent<HTMLElement>) =>
      beginInteraction(event, { type: "resize", edge }),
    onPointerMove: handlePointerMove,
    onPointerUp: handlePointerUp,
    onPointerCancel: cancelInteraction,
  });

  return {
    previewRect,
    snapZone,
    snapPreviewRect: snapZone ? snapRect(snapZone, viewport) : null,
    dragHandleProps,
    resizeHandleProps,
    isInteracting: previewRect !== null,
  };
}
