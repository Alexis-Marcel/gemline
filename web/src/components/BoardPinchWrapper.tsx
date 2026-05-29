import { TransformComponent, TransformWrapper } from "react-zoom-pan-pinch";
import type { ReactNode } from "react";

// Separate module so Board can React.lazy() it: desktop never downloads
// react-zoom-pan-pinch (~13 KB gzipped).
// doubleClick disabled because Board's tap-to-confirm already uses "second tap
// = commit"; the library's double-click-to-zoom would eat the confirmation.
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
