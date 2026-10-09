// PageSkeleton holds a page's place while its code downloads (the first visit to each
// page, since pages are split into their own chunks). The sidebar and top bar stay put;
// only the content area shows these placeholder blocks.
export function PageSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading page">
      <div className="flex items-center px-6 py-3.5" style={{ borderBottom: "1px solid var(--line)", background: "var(--bg)" }}>
        <div className="flex flex-col gap-1.5">
          <div className="h-[17px] w-[140px] animate-pulse rounded-md" style={{ background: "var(--panel-2)" }} />
          <div className="h-[10px] w-[90px] animate-pulse rounded" style={{ background: "var(--panel-2)" }} />
        </div>
      </div>
      <div className="mx-auto flex w-full max-w-[1200px] flex-col gap-4 px-4 py-6 sm:px-6">
        {[96, 180, 140].map((h, i) => (
          <div key={i} className="animate-pulse rounded-xl" style={{ height: h, background: "var(--panel)", border: "1px solid var(--line)" }} />
        ))}
      </div>
    </div>
  );
}
