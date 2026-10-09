import { useState } from "react";
import { api, type Book } from "../lib/api";
import { disposalLine, useRecycleMode } from "../lib/disposal";
import { ConfirmDialog } from "./ConfirmDialog";

// DeleteBookDialog is the one way to remove a book, from the grid or its page. Removing it
// from the library is the default; deleting its files is an explicit tick that says how
// much goes and whether it goes to the recycle bin or is gone for good.
export function DeleteBookDialog({ book, onClose, onDeleted }: {
  book: Book;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const mode = useRecycleMode();
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Every edition and extra audiobook the delete would take, from what the book already says.
  const files = [book.ebook, book.audiobook, ...(book.audio_versions ?? []).map((v) => v.file)].filter((f) => !!f && !!f.path);
  const bytes = files.reduce((n, f) => n + (f?.size_bytes ?? 0), 0);
  const hasFiles = book.has_file || files.length > 0;

  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      await api.deleteBook(book.id, deleteFiles);
      onDeleted();
    } catch (e) {
      setErr(`${(e as Error).message}\nThe book is still in your library.`);
      setBusy(false);
    }
  };

  const filesHint = !hasFiles
    ? "This book has no files on disk."
    : `The ebook and audiobook files. ${disposalLine(bytes, mode)}.`;

  return (
    <ConfirmDialog
      title={<>Remove “{book.title}”?</>}
      body="It'll be removed from your library and Arrmada will stop monitoring it. Its files stay on disk unless you tick the box below."
      extra={
        <label className="flex items-start gap-2.5 rounded-lg p-3 text-[12.5px]" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)", cursor: hasFiles ? "pointer" : "default", opacity: hasFiles ? 1 : 0.6 }}>
          <input type="checkbox" checked={deleteFiles} disabled={!hasFiles || busy} onChange={(e) => { setDeleteFiles(e.target.checked); setErr(null); }} className="mt-0.5" />
          <span>
            <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject)" : "var(--ink)" }}>Also delete files from disk</span>
            <span className="mt-0.5 block text-[11px] text-ink-faint">{filesHint}</span>
          </span>
        </label>
      }
      confirmLabel={deleteFiles ? "Remove + delete files" : "Remove"}
      busyLabel="Removing…"
      busy={busy}
      error={err}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}
