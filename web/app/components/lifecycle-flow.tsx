import { useId, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import { buildLifecycle, layoutLifecycle, nodeHeight, nodeWidth, type FlowAmount, type LifecycleData } from "~/lib/lifecycle";
import { formatAmount, formatDateTime } from "~/lib/format";
import { StatusText } from "./status";
import { Button } from "./ui/button";

function amountLabel(amount: FlowAmount) {
  return amount.exponent === undefined
    ? `${amount.value} ${amount.currency} minor units`
    : `${formatAmount(amount.value, amount.exponent)} ${amount.currency}`;
}

export function LifecycleFlow({ data, compact = false }: { data: LifecycleData; compact?: boolean }) {
  const graph = useMemo(() => buildLifecycle(data), [data]);
  const layout = useMemo(() => layoutLifecycle(graph), [graph]);
  const positions = new Map(layout.nodes.map((node) => [node.id, node]));
  const marker = useId().replaceAll(":", "");
  const [zoom, setZoom] = useState(0.8);
  const [hovered, setHovered] = useState<string | null>(null);
  const [focused, setFocused] = useState<string | null>(null);
  const active = focused ?? hovered;
  const connected = new Set(active ? [active, ...graph.edges.flatMap((edge) => edge.from === active ? [edge.to] : edge.to === active ? [edge.from] : [])] : []);
  const viewport = useRef<HTMLDivElement>(null);
  const main = positions.get(data.focus);

  function fit() {
    const width = (viewport.current?.clientWidth ?? 800) - 16;
    const height = (viewport.current?.clientHeight ?? 500) - 16;
    setZoom(Math.min(1, Math.max(0.4, Math.min(width / layout.width, height / layout.height))));
    viewport.current?.scrollTo({ left: 0, top: 0 });
  }

  return (
    <div className="min-w-0 border border-line">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3">
        <div className="flex items-center gap-3 text-xs">
          <span className="font-medium">Connected records</span>
          <span className="text-muted tabular-nums">{graph.nodes.length} nodes <span aria-hidden="true">·</span> {graph.edges.length} links</span>
        </div>
        <div role="group" aria-label="Flowchart zoom" className="flex items-center gap-1">
          <Button className="min-h-8 border-transparent px-2 text-base" aria-label="Zoom out" disabled={zoom <= 0.4} onClick={() => setZoom((value) => Math.max(0.4, value - 0.2))}>−</Button>
          <span className="w-10 text-center font-mono text-[11px] text-muted tabular-nums">{Math.round(zoom * 100)}%</span>
          <Button className="min-h-8 border-transparent px-2 text-base" aria-label="Zoom in" disabled={zoom >= 1.4} onClick={() => setZoom((value) => Math.min(1.4, value + 0.2))}>+</Button>
          <span aria-hidden="true" className="mx-2 h-4 border-l border-line" />
          <Button className="min-h-8" onClick={fit}>Fit</Button>
          {!compact && <Button className="min-h-8" onClick={() => {
            if (main && viewport.current) viewport.current.scrollTo({ left: Math.max(0, (main.x + nodeWidth / 2) * zoom - viewport.current.clientWidth / 2), top: Math.max(0, (main.y + nodeHeight / 2) * zoom - viewport.current.clientHeight / 2) });
          }}>Center</Button>}
        </div>
      </div>
      <div ref={viewport} role="region" aria-label="Lifecycle flowchart" tabIndex={0} className={`overflow-auto overscroll-contain bg-sunken ${compact ? "h-[23rem]" : "h-[min(65dvh,42rem)] min-h-80"}`}>
        <div className="grid min-h-full min-w-full place-items-center" style={{ width: layout.width * zoom, height: layout.height * zoom }}>
          <div style={{ width: layout.width * zoom, height: layout.height * zoom }}>
            <div className="relative origin-top-left" style={{ width: layout.width, height: layout.height, transform: `scale(${zoom})` }}>
              {layout.columns.map((column) => <div key={column.rank} aria-hidden="true" className="absolute flex items-center gap-3 border-b border-line pb-3 text-[11px] text-muted" style={{ left: column.x, top: 24, width: nodeWidth }}>
                <span className="font-mono">{String(column.rank + 1).padStart(2, "0")}</span>
                <span className="font-medium tracking-wide uppercase">{column.label}</span>
              </div>)}
              <svg aria-hidden="true" className="pointer-events-none absolute inset-0" width={layout.width} height={layout.height}>
                <defs><marker id={marker} markerWidth="7" markerHeight="7" refX="6" refY="3.5" orient="auto"><path d="M0 0 L7 3.5 L0 7" className="fill-muted" /></marker></defs>
                {graph.edges.map((edge) => {
                  const from = positions.get(edge.from)!;
                  const to = positions.get(edge.to)!;
                  const x1 = from.x + nodeWidth;
                  const y1 = from.y + nodeHeight / 2;
                  const x2 = to.x;
                  const y2 = to.y + nodeHeight / 2;
                  const middle = x2 - 64;
                  const highlighted = active === edge.from || active === edge.to;
                  return <g key={`${edge.from}:${edge.to}`} opacity={active && !highlighted ? 0.2 : 1} className="transition-opacity motion-reduce:transition-none">
                    <path d={`M${x1},${y1} H${middle} V${y2} H${x2 - 5}`} fill="none" className={highlighted ? "stroke-ink" : "stroke-line-strong"} strokeWidth={highlighted ? 2 : 1.5} markerEnd={`url(#${marker})`} />
                    <rect x={x2 - 111} y={y2 - 25} width="100" height="17" className="fill-sunken" />
                    <text x={x2 - 61} y={y2 - 13} textAnchor="middle" className="fill-muted text-[10px]">{edge.label}</text>
                  </g>;
                })}
              </svg>
              <ul aria-label="Lifecycle records" className="list-none">
                {layout.nodes.map((node) => {
                  const selected = node.id === data.focus;
                  return <li key={node.id} className="absolute transition-opacity motion-reduce:transition-none" style={{ left: node.x, top: node.y, width: nodeWidth, height: nodeHeight, opacity: active && !connected.has(node.id) ? 0.45 : 1 }}>
                    <Link to={node.to} onMouseEnter={() => setHovered(node.id)} onMouseLeave={() => setHovered(null)} onFocus={() => setFocused(node.id)} onBlur={() => setFocused(null)}
                      className={`group relative flex h-full flex-col border bg-canvas transition-colors hover:border-ink motion-reduce:transition-none ${selected ? "border-ink" : "border-line-strong"}`}>
                      {graph.edges.some((edge) => edge.to === node.id) && <span aria-hidden="true" className="absolute top-1/2 -left-[3px] size-[5px] -translate-y-1/2 border border-line-strong bg-canvas" />}
                      {graph.edges.some((edge) => edge.from === node.id) && <span aria-hidden="true" className="absolute top-1/2 -right-[3px] size-[5px] -translate-y-1/2 border border-line-strong bg-canvas" />}
                      <div className={`flex items-center justify-between border-b px-4 py-2 text-[10px] font-medium tracking-wide uppercase ${selected ? "border-ink bg-ink text-canvas" : "border-line text-muted"}`}>
                        <span>{node.kind}</span><span>{selected ? "Selected" : "↗"}</span>
                      </div>
                      <div className="flex min-h-0 flex-1 flex-col px-4 py-3">
                        <p className="truncate font-medium tracking-tight" title={node.label}>{node.label}</p>
                        <p className="mt-1 truncate font-mono text-[10px] text-muted" title={node.detail}>{node.detail}</p>
                        <p className="mt-3 truncate text-lg font-medium tracking-tight tabular-nums" title={node.amounts.map(amountLabel).join(" · ")}>
                          {node.amounts.length > 1 ? `${node.amounts.length} currencies` : node.amounts.map(amountLabel).join("") || "Scheduled"}
                        </p>
                        <div className="mt-auto flex items-center justify-between gap-2 pt-2 text-[10px]">
                          <StatusText status={node.status} className="font-medium" />
                          <time dateTime={node.timestamp} className="text-muted">{formatDateTime(node.timestamp)}</time>
                        </div>
                      </div>
                    </Link>
                  </li>;
                })}
              </ul>
            </div>
          </div>
        </div>
      </div>
      <div className="flex flex-wrap items-start gap-x-6 gap-y-2 border-t border-line px-4 py-3 text-xs text-muted">
        {data.limited && <span className="text-warning" title="Older links may be missing">Partial view</span>}
        {graph.hiddenEntries > 0 && <span title="Open the transaction to see all entries">{graph.hiddenEntries} hidden groups</span>}
          <details className="min-w-0 flex-1 basis-64">
            <summary className="cursor-pointer text-ink">Connections</summary>
            <ul className="mt-3 grid gap-2 border-l border-line pl-3">
              {graph.edges.map((edge) => <li key={`${edge.from}:${edge.to}`}>
                <Link className="underline underline-offset-2" to={positions.get(edge.from)!.to}>{positions.get(edge.from)!.kind}: {positions.get(edge.from)!.label}</Link>
                {` → ${edge.label.toLowerCase()} → `}
                <Link className="underline underline-offset-2" to={positions.get(edge.to)!.to}>{positions.get(edge.to)!.kind}: {positions.get(edge.to)!.label}</Link>
              </li>)}
            </ul>
          </details>
      </div>
    </div>
  );
}
