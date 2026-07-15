import { PageHeader } from "../components/PageHeader";

export function Placeholder({
  title,
  crumb,
  milestone,
  note,
}: {
  title: string;
  crumb?: string;
  milestone?: string;
  note?: string;
}) {
  return (
    <>
      <PageHeader title={title} crumb={crumb} />
      <div className="mx-auto grid w-full max-w-[900px] place-items-center px-6 py-24">
        <div className="max-w-[380px] text-center">
          <div
            className="mx-auto mb-4 grid h-11 w-11 place-items-center rounded-xl"
            style={{ background: "var(--accent-soft)", color: "var(--accent)" }}
          >
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
              <path d="M12 8v4l3 2" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
              <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="2" />
            </svg>
          </div>
          <h2 className="m-0 text-base font-bold">{title} isn’t built yet</h2>
          <p className="mx-auto mt-2 text-[12.5px] leading-relaxed text-ink-dim">
            {note ?? "This module is on the roadmap."}
            {milestone && (
              <>
                {" "}
                Planned for milestone{" "}
                <span className="font-mono font-semibold text-accent">{milestone}</span>.
              </>
            )}
          </p>
        </div>
      </div>
    </>
  );
}
