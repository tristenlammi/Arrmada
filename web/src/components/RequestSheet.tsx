import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api, type MediaRequest, type QualityProfileInfo } from "../lib/api";
import { isStaff, useMe } from "../lib/me";
import { posterThumb } from "../lib/img";
import { formatSeasons, libraryPath, mediaLabel, requestAge, requestStage } from "../lib/requestStage";
import { Button, Modal, StatusChip, useConfirm, useToast } from "../ui";
import { BookFormatBadge } from "./BookFormats";
import { refreshAttention } from "../lib/useAttention";

// RequestSheet is one request, opened from the Discover strip, a row on the Requests page
// or a notification's ?id= link: who asked and when, their note, where it has got to, and
// the actions this viewer may take — every one a plainly visible button.
//
// Staff approve with a profile, decline or delete a pending request, and open what an
// approved one became in the library. A requester withdraws their own pending request or
// stops following someone else's. The server enforces all of it; this only decides what
// is worth offering.
export function RequestSheet({ requestId, initial, onChanged, onClose }: {
  requestId: number;
  /** What the opener already has, shown while the fresh copy loads. */
  initial?: MediaRequest;
  /** Something changed (approved, declined, withdrawn…): the opener refreshes its list. */
  onChanged: () => void;
  onClose: () => void;
}) {
  const { user } = useMe();
  const staff = isStaff(user);
  const flash = useToast();
  const confirm = useConfirm();
  const [rq, setRq] = useState<MediaRequest | null>(initial ?? null);
  const [queueKnown, setQueueKnown] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [profiles, setProfiles] = useState<QualityProfileInfo[] | null>(null);
  const [profile, setProfile] = useState("");

  // The fresh copy: tracking moves on, and a list row may be minutes old.
  useEffect(() => {
    let alive = true;
    api.getRequest(requestId)
      .then((r) => { if (alive) { setRq(r.request); setQueueKnown(r.client_health?.ok ?? true); setLoadError(null); } })
      .catch((e: Error) => { if (alive) setLoadError(e.message); });
    return () => { alive = false; };
  }, [requestId]);

  const pending = rq?.status === "pending";
  const media = rq?.media_type;
  // Staff pick the profile for a pending request; the list is staff-only on the server.
  useEffect(() => {
    if (!staff || !pending || !media) return;
    let alive = true;
    api.qualityProfiles(media)
      .then((r) => { if (alive) setProfiles(r.profiles.filter((p) => p.media_type === media)); })
      .catch(() => { if (alive) setProfiles([]); });
    return () => { alive = false; };
  }, [staff, pending, media]);
  // Preselect what the requester asked for, else the default.
  useEffect(() => {
    if (!profiles || profile) return;
    const own = rq?.quality_profile && profiles.find((p) => p.key === rq.quality_profile);
    const pick = own || profiles.find((p) => p.is_default) || profiles[0];
    if (pick) setProfile(pick.key);
  }, [profiles, profile, rq?.quality_profile]);
  const groups = useMemo(() => {
    const all = profiles ?? [];
    return { hd: all.filter((p) => !is4K(p)), uhd: all.filter(is4K) };
  }, [profiles]);

  if (!rq) {
    return (
      <Modal onClose={onClose} ariaLabel="Request" variant="sheet" size="lg">
        <div className="p-6 text-[12.5px] text-ink-dim">{loadError ?? "Loading…"}</div>
      </Modal>
    );
  }

  const stage = requestStage(rq, queueKnown);
  const own = !!user && rq.requested_by === user.id;
  const following = rq.relation === "subscriber";
  const lib = staff && rq.status === "approved" ? libraryPath(rq) : null;

  // act runs one action: a toast on success, the server's message inline on failure.
  const act = async (key: string, fn: () => Promise<unknown>, ok: string, close: boolean) => {
    setBusy(key); setError(null);
    try {
      await fn();
      flash(ok);
      onChanged();
      // A staff Needs-you count may have moved; nothing is asked where nobody shows it.
      refreshAttention();
      if (close) { onClose(); return; }
      const r = await api.getRequest(rq.id).catch(() => null);
      if (r) { setRq(r.request); setQueueKnown(r.client_health?.ok ?? true); }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(null);
    }
  };
  const approve = () => act("approve", () => api.approveRequest(rq.id, { quality_profile: profile || undefined }), `Approved “${rq.title}” — searching now`, false);
  const decline = async () => {
    const yes = await confirm({
      title: `Decline “${rq.title}”${rq.requested_by_name ? ` requested by ${rq.requested_by_name}` : ""}?`,
      body: "They’ll be told.", confirmLabel: "Decline", tone: "danger",
    });
    if (yes) act("decline", () => api.declineRequest(rq.id), `Declined “${rq.title}”`, false);
  };
  const remove = async () => {
    const yes = await confirm({ title: `Delete the request for “${rq.title}”?`, body: "It’s removed for everyone following it. Nothing in the library changes.", confirmLabel: "Delete", tone: "danger" });
    if (yes) act("delete", () => api.deleteRequest(rq.id), "Request deleted", true);
  };
  const withdraw = async () => {
    const yes = await confirm({ title: `Withdraw your request for “${rq.title}”?`, confirmLabel: "Withdraw", tone: "danger" });
    if (yes) act("withdraw", () => api.deleteRequest(rq.id), `Withdrew “${rq.title}”`, true);
  };
  const unfollow = async () => {
    const yes = await confirm({ title: `Stop following “${rq.title}”?`, body: "You won’t hear about it any more. The request itself stays.", confirmLabel: "Stop following" });
    if (yes) act("unfollow", () => api.unsubscribeRequest(rq.id), `Stopped following “${rq.title}”`, true);
  };

  const big = "min-h-[40px]";
  return (
    <Modal onClose={onClose} ariaLabel={rq.title} variant="sheet" size="lg" dismissible={!busy}>
      <div className="flex gap-4 p-5 sm:p-6">
        <div className="h-[150px] w-[100px] flex-none overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
          {rq.poster_url && <img src={posterThumb(rq.poster_url)} alt="" className="h-full w-full object-cover" />}
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{mediaLabel(rq.media_type)}</span>
            {/* What was asked for: Read / Listen / Both for a book. */}
            {rq.media_type === "book" && <BookFormatBadge formats={rq.formats} />}
            {following && <StatusChip tone="faint">Following</StatusChip>}
          </div>
          <h2 className="m-0 mt-1.5 text-[18px] font-bold leading-tight">{rq.title}</h2>
          <div className="mt-0.5 font-mono text-[11px] text-ink-faint">
            {[rq.author, rq.year || "", rq.media_type === "series" ? formatSeasons(rq.seasons) || "All seasons" : ""].filter(Boolean).join(" · ")}
          </div>
          <div className="mt-2.5 flex flex-wrap items-center gap-2">
            <StatusChip tone={stage.tone}>{stage.badge}</StatusChip>
            <span className="text-[12px]" style={{ color: stage.detailTone ?? "var(--ink-dim)" }}>{stage.detail}</span>
          </div>
          <div className="mt-2.5 text-[12px] text-ink-dim">
            {staff && rq.requested_by_name ? <>Requested by <b className="text-ink">{rq.requested_by_name}</b></> : own ? "Your request" : following ? "Someone else asked for this; you’re following it" : "Requested"}
            {requestAge(rq.created_at) && <span className="text-ink-faint"> · {requestAge(rq.created_at)}</span>}
          </div>
        </div>
      </div>

      <div className="flex flex-col gap-4 px-5 pb-5 sm:px-6 sm:pb-6">
        {rq.note && (staff || own) && (
          <div className="rounded-lg p-3 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
            <div className="mb-1 font-mono text-[9.5px] uppercase text-ink-faint">Note</div>
            <div className="whitespace-pre-wrap text-ink-dim">{rq.note}</div>
          </div>
        )}
        {staff && rq.followers && rq.followers.length > 0 && (
          <div className="text-[12px] text-ink-dim">
            <span className="font-mono text-[9.5px] uppercase text-ink-faint">Also following · </span>
            {rq.followers.map((f) => f.name).join(", ")}
          </div>
        )}

        {staff && pending && (
          <label className="flex flex-col gap-1 text-[12px]">
            <span className="font-mono text-[9.5px] uppercase text-ink-faint">Quality profile</span>
            <select
              value={profile}
              onChange={(e) => setProfile(e.target.value)}
              disabled={!profiles || profiles.length === 0}
              className={`${big} rounded-lg px-3 text-[12.5px]`}
              style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
            >
              {!profiles && <option value="">Loading profiles…</option>}
              {groups.hd.map((p) => <option key={p.key} value={p.key}>{p.name}{p.is_default ? " (default)" : ""}</option>)}
              {groups.uhd.length > 0 && (
                <optgroup label="4K">
                  {groups.uhd.map((p) => <option key={p.key} value={p.key}>{p.name}{p.is_default ? " (default)" : ""}</option>)}
                </optgroup>
              )}
            </select>
          </label>
        )}
        {/* Season trimming for a series request belongs here, above Approve. */}

        {error && <div className="text-[12px] font-medium" style={{ color: "var(--reject)" }} role="alert">{error}</div>}

        <div className="flex flex-wrap gap-2">
          {staff && pending && (
            <>
              <Button variant="primary" className={big} onClick={approve} busy={busy === "approve"} busyLabel="Approving…" disabled={!!busy}>Approve</Button>
              <Button className={big} onClick={decline} busy={busy === "decline"} busyLabel="Declining…" disabled={!!busy}>Decline</Button>
            </>
          )}
          {lib && <Link to={lib} className={`${big} inline-flex items-center rounded-lg px-4 text-[12.5px] font-semibold`} style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>Open in library</Link>}
          {!staff && own && pending && <Button className={big} onClick={withdraw} busy={busy === "withdraw"} disabled={!!busy}>Withdraw</Button>}
          {following && <Button className={big} onClick={unfollow} busy={busy === "unfollow"} disabled={!!busy}>Stop following</Button>}
          {staff && <Button variant="ghost" className={big} onClick={remove} busy={busy === "delete"} disabled={!!busy}>Delete</Button>}
          <Button variant="ghost" className={`${big} ml-auto`} onClick={onClose} disabled={!!busy}>Close</Button>
        </div>
      </div>
    </Modal>
  );
}

// is4K: a profile aimed at 2160p, grouped apart in the picker.
function is4K(p: QualityProfileInfo): boolean {
  return /2160|\b4k\b|uhd/i.test(`${p.name} ${p.key}`);
}
