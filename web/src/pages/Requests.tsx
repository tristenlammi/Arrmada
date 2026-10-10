import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { RequestSheet } from "../components/RequestSheet";
import { api, type MediaRequest, type RequestCounts, type RequestSection } from "../lib/api";
import { isStaff, useMe } from "../lib/me";
import { posterThumb } from "../lib/img";
import { usePoll } from "../lib/usePoll";
import { pickTab } from "../lib/useTabParam";
import { formatSeasons, mediaLabel, MOVING_STAGES, requestAge, requestStage } from "../lib/requestStage";
import { BookFormatBadge } from "../components/BookFormats";
import { refreshAttention } from "../lib/useAttention";
import { TabPanel, Tabs } from "../ui/Tabs";
import { Button, EmptyState, ErrorState, StatusChip, useConfirm, useToast } from "../ui";

// The Requests page: every request in sections, for staff to decide and for requesters to
// follow their own (and the ones they joined). Everything that shapes the view lives in the
// address so a reload, Back or a link lands on the same thing:
//
//   ?tab=needs|active|ready|declined   the section (needs = waiting for approval)
//   ?type=movie|series|book            a media filter
//   ?q=                                a title search
//   ?id=<request id>                   the RequestSheet open on one request
//
// Links from elsewhere (the Needs-you feed, notifications) use /requests?tab=needs for
// what's waiting, or /requests?id=<id> for one request. ?section=pending (or the API's own
// section names) is accepted as a synonym for the tab.

type Tab = "needs" | "active" | "ready" | "declined";
const TAB_SECTION: Record<Tab, RequestSection> = { needs: "needs_approval", active: "in_progress", ready: "ready", declined: "declined" };
const SECTION_TAB: Record<string, Tab> = {
  needs_approval: "needs", pending: "needs", needs: "needs",
  in_progress: "active", active: "active", approved: "active",
  ready: "ready", available: "ready", declined: "declined",
};
const TABS: Tab[] = ["needs", "active", "ready", "declined"];
const PAGE = 50;
const TYPES = [
  { key: "", label: "All" },
  { key: "movie", label: "Movies" },
  { key: "series", label: "TV" },
  { key: "book", label: "Books" },
] as const;

export function Requests({ chrome = true }: { chrome?: boolean }) {
  const { user, booksEnabled } = useMe();
  const staff = isStaff(user);
  const flash = useToast();
  const confirm = useConfirm();
  const [params, setParams] = useSearchParams();

  const [counts, setCounts] = useState<RequestCounts | null>(null);
  const [items, setItems] = useState<MediaRequest[] | null>(null);
  const [total, setTotal] = useState(0);
  const [queueKnown, setQueueKnown] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [rowErrors, setRowErrors] = useState<Record<number, string>>({});
  const [busy, setBusy] = useState<string | null>(null);

  // The tab: ?tab=, or a ?section= synonym; without either, staff land on what's waiting
  // when anything is (once the counts are in), everyone else on what's in progress.
  const asked = params.get("tab") ?? params.get("section");
  const fallback: Tab = staff && (counts?.needs_approval ?? 0) > 0 ? "needs" : "active";
  const tab = pickTab(asked ? (SECTION_TAB[asked] ?? asked) : null, TABS, fallback);
  const type = params.get("type") ?? "";
  const q = params.get("q") ?? "";
  const openID = Number(params.get("id")) || 0;
  const [qInput, setQInput] = useState(q);
  useEffect(() => { setQInput(q); }, [q]);

  const setParam = (key: string, value: string, replace = false) =>
    setParams((p) => {
      const next = new URLSearchParams(p);
      if (value) next.set(key, value); else next.delete(key);
      if (key === "tab") next.delete("section");
      return next;
    }, { replace });

  // How many rows are showing: grows with Load more, back to one page on a new view.
  const view = `${tab}|${type}|${q}`;
  const [more, setMore] = useState({ view, n: PAGE });
  const limit = more.view === view ? more.n : PAGE;
  const loadMore = () => setMore({ view, n: limit + PAGE });
  useEffect(() => { setSelected(new Set()); setRowErrors({}); }, [view]);

  const load = useCallback(async () => {
    try {
      const r = await api.requests({ section: TAB_SECTION[tab], limit, media_type: type || undefined, q: q || undefined });
      setItems(r.requests);
      setTotal(r.total ?? r.requests.length);
      setCounts(r.counts ?? null);
      setQueueKnown(r.client_health?.ok ?? true);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [tab, limit, type, q]);
  // Something in flight moves on its own; otherwise a quarter-minute is fresh enough.
  const moving = (items ?? []).some((rq) => MOVING_STAGES.has(rq.tracking?.stage ?? ""));
  // The effect loads each new view at once; the poll only repeats (paused in a hidden tab).
  usePoll(load, moving ? 8000 : 15000, { immediate: false });
  useEffect(() => { void load(); }, [load]);

  const pendingIDs = useMemo(() => (items ?? []).filter((rq) => rq.status === "pending").map((rq) => rq.id), [items]);
  const selectable = staff && tab === "needs";
  const allSelected = selectable && pendingIDs.length > 0 && pendingIDs.every((id) => selected.has(id));
  const toggle = (id: number) => setSelected((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const toggleAll = () => setSelected(allSelected ? new Set() : new Set(pendingIDs));

  // One approve or decline, from a row: a decline always asks first.
  const decide = async (rq: MediaRequest, action: "approve" | "decline") => {
    if (action === "decline") {
      const yes = await confirm({ title: `Decline “${rq.title}”${rq.requested_by_name ? ` requested by ${rq.requested_by_name}` : ""}?`, body: "They’ll be told.", confirmLabel: "Decline", tone: "danger" });
      if (!yes) return;
    }
    setBusy(`${action}:${rq.id}`);
    setRowErrors((e) => { const n = { ...e }; delete n[rq.id]; return n; });
    try {
      if (action === "approve") await api.approveRequest(rq.id);
      else await api.declineRequest(rq.id);
      flash(action === "approve" ? `Approved “${rq.title}” — searching now` : `Declined “${rq.title}”`);
      refreshAttention(); // the sidebar's waiting count follows at once
      await load();
    } catch (e) {
      setRowErrors((errs) => ({ ...errs, [rq.id]: (e as Error).message }));
    } finally {
      setBusy(null);
    }
  };

  // Bulk: each request is decided on its own server-side; a failure stays on its row.
  const bulk = async (action: "approve" | "decline") => {
    const ids = [...selected];
    if (ids.length === 0) return;
    if (action === "decline") {
      const yes = await confirm({ title: `Decline ${ids.length} request${ids.length === 1 ? "" : "s"}?`, body: "Each requester is told.", confirmLabel: `Decline ${ids.length}`, tone: "danger" });
      if (!yes) return;
    }
    setBusy(`bulk:${action}`);
    try {
      const r = await api.bulkRequests({ action, ids });
      const failed: Record<number, string> = {};
      for (const res of r.results) if (!res.ok) failed[res.id] = res.error || "Didn’t go through";
      const ok = r.results.length - Object.keys(failed).length;
      setRowErrors(failed);
      setSelected(new Set(Object.keys(failed).map(Number)));
      flash(`${action === "approve" ? "Approved" : "Declined"} ${ok} of ${ids.length}`, Object.keys(failed).length ? { tone: "error" } : undefined);
      refreshAttention();
      await load();
    } catch (e) {
      flash((e as Error).message, { tone: "error" });
    } finally {
      setBusy(null);
    }
  };

  // The middle tab names a request section, not a library status (lib/status).
  const active = "In progress"; // copy-ok: a request section, not a library status
  const tabLabel: Record<Tab, string> = staff
    ? { needs: "Needs approval", active, ready: "Ready", declined: "Declined" }
    : { needs: "Waiting", active, ready: "Ready", declined: "Declined" };
  const countOf: Record<Tab, number | undefined> = {
    needs: counts?.needs_approval, active: counts?.in_progress, ready: counts?.ready, declined: counts?.declined,
  };
  const types = booksEnabled ? TYPES : TYPES.filter((t) => t.key !== "book");
  const open = openID ? (items ?? []).find((rq) => rq.id === openID) : undefined;

  return (
    <>
      {chrome && <PageHeader title="Requests" />}
      <div className="mx-auto w-full max-w-[1200px] px-4 py-5 sm:px-6">
        {!chrome && <h1 className="m-0 mb-3 text-[17px] font-bold">{staff ? "Requests" : "Your requests"}</h1>}
        <Tabs
          tabs={TABS.map((t) => ({ key: t, label: tabLabel[t], count: countOf[t] }))}
          value={tab}
          onChange={(t) => setParam("tab", t)}
          idPrefix="requests"
          label="Request sections"
          className="mb-4 border-b"
        />

        <div className="mb-4 flex flex-wrap items-center gap-2">
          <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }} role="group" aria-label="Media type">
            {types.map((t) => (
              <button
                key={t.key}
                onClick={() => setParam("type", t.key)}
                aria-pressed={type === t.key}
                className="min-h-[32px] rounded-md px-3 text-[11.5px] font-semibold"
                style={{ background: type === t.key ? "var(--accent)" : "transparent", color: type === t.key ? "var(--accent-ink)" : "var(--ink-faint)" }}
              >
                {t.label}
              </button>
            ))}
          </div>
          <form className="min-w-0 flex-1 sm:max-w-[280px]" onSubmit={(e) => { e.preventDefault(); setParam("q", qInput.trim()); }}>
            <input
              type="search"
              value={qInput}
              onChange={(e) => { setQInput(e.target.value); if (!e.target.value) setParam("q", "", true); }}
              placeholder="Search titles"
              aria-label="Search requests by title"
              className="min-h-[34px] w-full rounded-lg px-3 text-[12.5px]"
              style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
            />
          </form>
        </div>

        <TabPanel idPrefix="requests" value={tab}>
          {error && !items ? (
            <ErrorState what="requests" message={error} onRetry={() => { void load(); }} />
          ) : !items ? (
            <div className="py-10 text-center text-[12.5px] text-ink-dim">Loading…</div>
          ) : items.length === 0 ? (
            <EmptyState title={emptyTitle(tab, staff, !!(q || type))} body={emptyBody(tab, staff, !!(q || type))} />
          ) : (
            <>
              {selectable && (
                <label className="mb-2 flex min-h-[36px] w-fit items-center gap-2 text-[12px] text-ink-dim">
                  <input type="checkbox" checked={allSelected} onChange={toggleAll} className="h-4 w-4" />
                  Select all on this page
                </label>
              )}
              <ul className="m-0 flex list-none flex-col gap-2 p-0">
                {items.map((rq) => (
                  <RequestRow
                    key={rq.id}
                    rq={rq}
                    staff={staff}
                    own={!!user && rq.requested_by === user.id}
                    queueKnown={queueKnown}
                    selectable={selectable && rq.status === "pending"}
                    selected={selected.has(rq.id)}
                    onSelect={() => toggle(rq.id)}
                    busy={busy}
                    error={rowErrors[rq.id]}
                    onOpen={() => setParam("id", String(rq.id))}
                    onDecide={(a) => decide(rq, a)}
                  />
                ))}
              </ul>
              {items.length < total && (
                <div className="mt-4 flex justify-center">
                  <Button onClick={loadMore}>Load more ({total - items.length} more)</Button>
                </div>
              )}
            </>
          )}
        </TabPanel>
      </div>

      {/* The bulk bar sits at the bottom of the screen while anything is ticked. */}
      {selectable && selected.size > 0 && (
        <div className="sticky bottom-0 z-20 flex flex-wrap items-center justify-center gap-2 px-4 py-3" style={{ background: "var(--panel)", borderTop: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
          <span className="text-[12px] text-ink-dim">{selected.size} selected</span>
          <Button variant="primary" className="min-h-[40px]" onClick={() => bulk("approve")} busy={busy === "bulk:approve"} busyLabel="Approving…" disabled={!!busy}>Approve {selected.size}</Button>
          <Button className="min-h-[40px]" onClick={() => bulk("decline")} busy={busy === "bulk:decline"} busyLabel="Declining…" disabled={!!busy}>Decline {selected.size}</Button>
          <Button variant="ghost" className="min-h-[40px]" onClick={() => setSelected(new Set())} disabled={!!busy}>Clear</Button>
        </div>
      )}

      {openID > 0 && (
        <RequestSheet
          key={openID}
          requestId={openID}
          initial={open}
          onChanged={() => { void load(); }}
          onClose={() => setParam("id", "")}
        />
      )}
    </>
  );
}

function RequestRow({ rq, staff, own, queueKnown, selectable, selected, onSelect, busy, error, onOpen, onDecide }: {
  rq: MediaRequest; staff: boolean; own: boolean; queueKnown: boolean;
  selectable: boolean; selected: boolean; onSelect: () => void;
  busy: string | null; error?: string;
  onOpen: () => void; onDecide: (a: "approve" | "decline") => void;
}) {
  const stage = requestStage(rq, queueKnown);
  const pending = rq.status === "pending";
  return (
    <li className="rounded-xl p-3" style={{ background: "var(--panel)", border: `1px solid ${error ? "var(--reject)" : "var(--line)"}` }}>
      <div className="flex items-start gap-3">
        {selectable && (
          <input type="checkbox" checked={selected} onChange={onSelect} aria-label={`Select ${rq.title}`} className="mt-1 h-4 w-4 flex-none" />
        )}
        <button onClick={onOpen} className="flex min-w-0 flex-1 items-start gap-3 text-left" aria-label={`Open the request for ${rq.title}`}>
          <div className="h-[72px] w-[48px] flex-none overflow-hidden rounded-lg" style={{ background: "var(--panel-2)" }}>
            {rq.poster_url && <img src={posterThumb(rq.poster_url)} alt="" className="h-full w-full object-cover" loading="lazy" decoding="async" />}
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{mediaLabel(rq.media_type)}</span>
              <span className="min-w-0 truncate text-[13.5px] font-semibold" style={{ color: "var(--ink)" }}>{rq.title}</span>
              <span className="font-mono text-[10.5px] text-ink-faint">{rq.year || ""}</span>
              {rq.author && <span className="min-w-0 truncate text-[11px] text-ink-dim">{rq.author}</span>}
              {rq.media_type === "book" && <BookFormatBadge formats={rq.formats} />}
              {rq.media_type === "series" && rq.seasons && rq.seasons.length > 0 && (
                <span className="font-mono text-[10.5px] text-ink-dim">{formatSeasons(rq.seasons)}</span>
              )}
              {rq.relation === "subscriber" && <StatusChip tone="faint">Following</StatusChip>}
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px]">
              <StatusChip tone={stage.tone}>{stage.badge}</StatusChip>
              <span style={{ color: stage.detailTone ?? "var(--ink-dim)" }}>{stage.detail}</span>
            </div>
            <div className="mt-1 truncate font-mono text-[10px] text-ink-faint">
              {staff && rq.requested_by_name ? `${rq.requested_by_name} · ` : own && !staff ? "" : ""}{requestAge(rq.created_at)}
            </div>
            {rq.note && (staff || own) && <div className="mt-1 line-clamp-1 text-[11.5px] italic text-ink-dim">“{rq.note}”</div>}
          </div>
        </button>
        {staff && pending && (
          <div className="flex flex-none flex-col gap-1.5 sm:flex-row">
            <Button variant="primary" size="sm" className="min-h-[36px]" onClick={() => onDecide("approve")} busy={busy === `approve:${rq.id}`} disabled={!!busy}>Approve</Button>
            <Button size="sm" className="min-h-[36px]" onClick={() => onDecide("decline")} busy={busy === `decline:${rq.id}`} disabled={!!busy}>Decline</Button>
          </div>
        )}
      </div>
      {error && <div className="mt-2 text-[11.5px] font-medium" style={{ color: "var(--reject)" }} role="alert">{error}</div>}
    </li>
  );
}

function emptyTitle(tab: Tab, staff: boolean, filtered: boolean): string {
  if (filtered) return "Nothing matches";
  switch (tab) {
    case "needs": return staff ? "Nothing waiting for approval" : "Nothing waiting";
    case "active": return "Nothing in progress";
    case "ready": return "Nothing ready yet";
    case "declined": return "Nothing declined";
  }
}

function emptyBody(tab: Tab, staff: boolean, filtered: boolean): string {
  if (filtered) return "Try another type or search.";
  if (staff) return tab === "needs" ? "New requests show up here for you to approve or decline." : "";
  return tab === "needs" || tab === "active" ? "Find something on Discover and request it — it shows up here." : "";
}
