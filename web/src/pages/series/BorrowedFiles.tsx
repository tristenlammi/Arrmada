import { useEffect, useRef, useState } from "react";
import { Button, ConfirmDialog } from "../../ui";
import { useQuery } from "../../lib/query";
import { seriesApi, type BorrowedFile } from "../../lib/seriesApi";

const base = (p: string) => p.slice(Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\")) + 1);
const ep = (it: BorrowedFile) => `S${String(it.season).padStart(2, "0")}E${String(it.episode).padStart(2, "0")}`;
const files = (n: number) => `${n} file${n === 1 ? "" : "s"}`;

// BorrowedBanner says when files recorded for the show look like another library show's —
// what an early library scan left when it matched a folder to the wrong show — and offers
// to forget them. Nothing is forgotten until the list has been seen and confirmed: a
// sequel kept under the first show's name looks the same and is the show's own.
export function BorrowedBanner({ seriesId, title, monitored, refreshKey, onChange, flash }: {
  seriesId: number; title: string; monitored: boolean; refreshKey?: number; onChange: () => void; flash: (msg: string) => void;
}) {
  const q = useQuery(`series-borrowed:${seriesId}`, () => seriesApi.borrowedFiles(seriesId), { staleMs: 0 });
  const { refetch } = q;
  const mounted = useRef(false);
  useEffect(() => {
    if (!mounted.current) { mounted.current = true; return; }
    void refetch();
  }, [refreshKey, refetch]);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const items = q.data?.items ?? [];
  if (items.length === 0) return null;
  const owners = [...new Set(items.map((it) => it.owner))];
  const theirs = owners.length === 1 ? `“${owners[0]}”` : "other shows";
  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-3 rounded-lg px-3 py-2.5 text-base" style={{ border: "1px solid var(--accent-line)", background: "var(--accent-soft)" }}>
        <span className="min-w-0 flex-1" style={{ color: "var(--ink)" }}>
          {files(items.length)} recorded for this show {items.length === 1 ? "looks" : "look"} like {items.length === 1 ? "it belongs" : "they belong"} to {theirs}. While they're recorded here, new episodes may be held for review.
        </span>
        <Button size="sm" variant="primary" onClick={() => { setError(null); setOpen(true); }}>Review</Button>
      </div>
      {open && (
        <ConfirmDialog
          title={<>Forget {files(items.length)} for “{title}”?</>}
          body={
            <div>
              <p className="m-0">
                These are recorded as episodes of “{title}”, but their names say they belong to {theirs}. Forgetting them removes them from this show only. Nothing on disk is touched, and {theirs} {owners.length === 1 ? "keeps" : "keep"} them.
                {monitored ? " Searching is on for this show, so Arrmada will look for those episodes again." : ""}
              </p>
              <p className="mb-0 mt-2">
                If they really are this show's own files under another name, cancel and add that name as an alias on the show instead.
              </p>
              <ul className="mb-0 mt-3 max-h-64 list-none overflow-auto p-0 text-sm" style={{ color: "var(--ink-dim)" }}>
                {items.map((it) => (
                  <li key={`${it.season}-${it.episode}`} className="truncate py-0.5" title={it.path}>
                    <span className="font-mono">{ep(it)}</span> · {base(it.path)}
                  </li>
                ))}
              </ul>
            </div>
          }
          confirmLabel={`Forget ${files(items.length)}`}
          busyLabel="Forgetting…"
          busy={busy}
          error={error}
          onConfirm={async () => {
            setBusy(true); setError(null);
            try {
              const { cleared } = await seriesApi.forgetBorrowedFiles(seriesId, items.map((it) => it.path));
              setOpen(false);
              void refetch();
              onChange();
              flash(cleared === 0 ? "Nothing was forgotten: those records had already changed." : `Forgot ${files(cleared)}. Nothing on disk was touched.`);
            } catch (e) {
              setError((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
          onCancel={() => setOpen(false)}
        />
      )}
    </>
  );
}
