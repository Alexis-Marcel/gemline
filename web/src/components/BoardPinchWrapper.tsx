import { TransformComponent, TransformWrapper } from "react-zoom-pan-pinch";
import type { ReactNode } from "react";

/**
 * BoardPinchWrapper is the touch-only wrapper that gives Board its
 * pinch-zoom + pan behaviour. Extracted into its own module so Board
 * can React.lazy() it: mouse / desktop users never download
 * react-zoom-pan-pinch (~40 KB raw, ~13 KB gzipped), and even on
 * mobile the chunk only loads when the Board mounts.
 *
 * Configuration mirrors the previous inline usage:
 *  - initialScale=1, minScale=1, maxScale=3 — anything beyond 3× is
 *    more disorienting than useful given the cell density;
 *  - doubleClick.disabled — Board's tap-to-confirm flow already uses
 *    "second tap = commit"; the library's default double-click-to-zoom
 *    would eat the confirmation;
 *  - wheel.disabled — coarse-only path; setting it for safety in case
 *    the matchMedia check ever lies (e.g. hybrid devices);
 *  - panning.velocityDisabled — kill the inertia drift; on a tight
 *    board you want the view to stop exactly where you lift.
 */
export default function BoardPinchWrapper({ children }: { children: ReactNode }) {
  return (
    <TransformWrapper
      initialScale={1}
      minScale={1}
      maxScale={3}
      doubleClick={{ disabled: true }}
      wheel={{ disabled: true }}
      panning={{ velocityDisabled: true }}
    >
      <TransformComponent wrapperClass="!w-full !h-full" contentClass="!w-full !h-full">
        {children}
      </TransformComponent>
    </TransformWrapper>
  );
}
