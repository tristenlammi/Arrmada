export function PageHeader({ title, crumb }: { title: string; crumb?: string }) {
  return (
    <div
      className="sticky top-0 z-30 flex items-center gap-4 px-6 py-3.5"
      style={{ borderBottom: "1px solid var(--line)", background: "var(--bg)" }}
    >
      <div>
        <h1 className="m-0 text-[17px] font-bold tracking-[-0.01em]">{title}</h1>
        {crumb && <div className="font-mono text-[11px] text-ink-faint">{crumb}</div>}
      </div>
    </div>
  );
}
