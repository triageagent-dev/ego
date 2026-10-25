import React, { useEffect, useRef, useState } from 'react';
import type { Plugin } from 'chart.js';
import { ZOOM } from '../../lib/zoom';

interface ChartProps {
  width?: number;
  height?: number;
  options?: Record<string, unknown>;
  plugins?: Plugin[];
}

// chart.js turns the pointer into chart pixels by dividing the offset in client pixels (zoomed) by the chart's own
// width (layout pixels), so under the page's CSS zoom it reads the pointer ZOOM times too far right and the tooltip
// lands on another bar. The offset is rescaled by what the canvas actually measures on screen, and the check for
// whether the pointer is inside the plot is redone with the corrected position.
const zoomedPointer: Plugin = {
  id: 'zoomedPointer',
  beforeEvent(chart, args) {
    const native = args.event.native as (MouseEvent & Partial<TouchEvent>) | null;
    if (!native) return;
    const point = native.touches?.[0] ?? native.changedTouches?.[0] ?? native;
    if (typeof point.clientX !== 'number') return;
    const rect = chart.canvas.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    args.event.x = (point.clientX - rect.left) / (rect.width / chart.width);
    args.event.y = (point.clientY - rect.top) / (rect.height / chart.height);
    args.inChartArea = chart.isPointInArea({ x: args.event.x, y: args.event.y });
  },
};

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
          plugins: [...(children.props.plugins ?? []), zoomedPointer],
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
