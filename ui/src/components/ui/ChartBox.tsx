import React, { useEffect, useRef, useState } from 'react';
import { ZOOM } from '../../lib/zoom';

interface ChartProps {
  width?: number;
  height?: number;
  options?: Record<string, unknown>;
}

// Holds one chart.js chart and sizes it to the box. chart.js measures its container with getBoundingClientRect, which
// includes the page's CSS zoom, so inside the zoomed layout it draws the canvas ZOOM times too large and the chart
// spills out of its card. The box measures itself in layout pixels (clientWidth is not zoomed) and hands the chart
// that size; the canvas is drawn at ZOOM times the device ratio, so it stays sharp when the zoom scales it up.
export const ChartBox: React.FC<{ className?: string; children: React.ReactElement<ChartProps> }> = ({ className, children }) => {
  const ref = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState<{ width: number; height: number } | null>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const next = { width: el.clientWidth, height: el.clientHeight };
      setSize((cur) => (cur && cur.width === next.width && cur.height === next.height ? cur : next));
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  return (
    <div className={className} ref={ref} style={{ position: 'relative' }}>
      {size &&
        size.width > 0 &&
        React.cloneElement(children, {
          width: size.width,
          height: size.height,
          options: {
            ...children.props.options,
            responsive: false,
            maintainAspectRatio: false,
            devicePixelRatio: window.devicePixelRatio * ZOOM,
          },
        })}
    </div>
  );
};
