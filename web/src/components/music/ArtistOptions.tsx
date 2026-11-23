import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { musicApi, type ApplyPlan, type ArtistOptions as Options, type MonitorNew, type ReleaseTypeChoices, type ReleaseTypes } from "../../lib/musicApi";
import { Button, useConfirm, useToast } from "../../ui";
import { Field } from "../../ui/Field";
import { Select } from "../../ui/Select";
import { Switch } from "../../ui/Switch";
import { ReleaseTypePicker, sameTypes } from "./releaseRules";

// An artist's options (MUS-14): what happens to new releases, and which kinds of release
// are listed. Saving changes what NEW albums get and what the album list shows (kinds the
// types leave out are hidden unless wanted or holding files); it never wants or unwants an
// album by itself. Applying the types to the albums already listed is a
// separate step that shows what it would do first ("Want matching albums").

const NEW_RELEASE_WORDS: Record<MonitorNew, string> = {
  all: "Want them",
  none: "Just list them",
};

const DEFAULTS_LINK = "/settings/media#music-releases";

function planWords(p: ApplyPlan): string[] {
  const out: string[] = [];
  if (p.want.length) out.push(`want ${p.want.length} album${p.want.length === 1 ? "" : "s"} these types allow`);
  if (p.unwant.length) out.push(`stop wanting ${p.unwant.length} they no longer allow (none holds files)`);
  if (p.singles.length) out.push(`check ${p.singles.length} single${p.singles.length === 1 ? "" : "s"} against the albums, wanting the ones that aren't on one`);
  return out;
}

function titles(list: { title: string; year?: number }[]): string {
  const names = list.slice(0, 8).map((a) => (a.year ? `${a.title} (${a.year})` : a.title));
  return names.join(", ") + (list.length > 8 ? ` and ${list.length - 8} more` : "");
}

export function ArtistOptions({ artistId, onChanged }: { artistId: number; onChanged: () => void }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [opts, setOpts] = useState<Options | null>(null);
  const [choices, setChoices] = useState<ReleaseTypeChoices | null>(null);
  const [error, setError] = useState<string | null>(null);
  // The form: "" follows the library default; types null follows it too.
  const [monitorNew, setMonitorNew] = useState<"" | MonitorNew>("");
  const [types, setTypes] = useState<ReleaseTypes | null>(null);
  const [saving, setSaving] = useState(false);
  const [applying, setApplying] = useState(false);

  const fill = (o: Options) => {
    setOpts(o);
    setMonitorNew(o.monitor_new);
    setTypes(o.release_types);
  };
  const load = useCallback(() => {
    musicApi
      .artistOptions(artistId)
      .then((r) => {
        fill(r.options);
        setChoices(r.choices);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }, [artistId]);
  useEffect(() => {
    load();
  }, [load]);

  if (error) return <p className="text-sm" style={{ color: "var(--reject-text)" }}>{error}</p>;
  if (!opts || !choices) return <p className="text-sm text-ink-dim">Loading…</p>;

  const dirty =
    monitorNew !== opts.monitor_new ||
    (types === null) !== (opts.release_types === null) ||
    (types !== null && opts.release_types !== null && !sameTypes(types, opts.release_types));

  const save = async () => {
    setSaving(true);
    try {
      const r = await musicApi.setArtistOptions(artistId, { monitor_new: monitorNew, release_types: types });
      fill(r.options);
      toast(r.refreshed ? "Saved. Albums the new types allow are listed, not wanted." : "Saved.");
      if (r.refreshed) onChanged();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setSaving(false);
    }
  };

  const apply = async () => {
    const plan = opts.apply;
    const parts = planWords(plan);
    const ok = await confirm({
      title: "Apply the release types to the albums already listed?",
      body: (
        <div className="flex flex-col gap-2 text-sm">
          <p className="m-0">This will {parts.join(", ")}.</p>
          {plan.want.length > 0 && <p className="m-0">Wanted: {titles(plan.want)}</p>}
          {plan.unwant.length > 0 && <p className="m-0">No longer wanted: {titles(plan.unwant)}</p>}
          {plan.singles.length > 0 && <p className="m-0">Singles to check: {titles(plan.singles)}</p>}
          <p className="m-0 text-ink-faint">Wanted albums are searched by the next sweep.</p>
        </div>
      ),
      confirmLabel: "Apply",
    });
    if (!ok) return;
    setApplying(true);
    try {
      await musicApi.applyArtistOptions(artistId, plan);
      toast("Applied.");
      onChanged();
      load();
    } catch (e) {
      // 409: the albums changed since this plan was shown. Show the new one.
      toast((e as Error).message, { tone: "error" });
      load();
    } finally {
      setApplying(false);
    }
  };

  const followTypes = types === null;
  const plan = planWords(opts.apply);
  return (
    <div className="flex flex-col gap-4 rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <Field
        label="New releases"
        hint="Albums MusicBrainz newly lists for this artist. Older music it only now lists, and anything a change of types brings in, is listed but never wanted on its own."
      >
        <Select value={monitorNew} onChange={(e) => setMonitorNew(e.target.value as "" | MonitorNew)} className="w-full max-w-[320px] px-3 py-2 text-base">
          <option value="">Library default ({NEW_RELEASE_WORDS[opts.default_monitor_new].toLowerCase()})</option>
          <option value="all">{NEW_RELEASE_WORDS.all}</option>
          <option value="none">{NEW_RELEASE_WORDS.none}</option>
        </Select>
      </Field>

      <div className="flex flex-col gap-3">
        <Switch
          size="sm"
          checked={followTypes}
          onChange={(on) => setTypes(on ? null : { ...opts.effective_release_types })}
        >
          Release types: use the library default
        </Switch>
        {followTypes ? (
          <p className="m-0 text-xs text-ink-faint">
            {opts.default_release_types.primary.join(", ")}
            {opts.default_release_types.secondary.length > 0 ? `, with ${opts.default_release_types.secondary.join(", ")}` : ", studio releases only"} (
            <Link to={DEFAULTS_LINK} className="underline">change the default</Link>)
          </p>
        ) : (
          <ReleaseTypePicker idPrefix={`artist-${artistId}`} value={types} choices={choices} onChange={setTypes} disabled={saving} />
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" variant="primary" onClick={() => void save()} disabled={!dirty} busy={saving} busyLabel="Saving…">
          Save options
        </Button>
        {!dirty && (
          <span className="text-xs text-ink-faint">
            The album list shows only these kinds, plus albums that are wanted or have files. Nothing is wanted or unwanted by itself.
          </span>
        )}
      </div>

      <div className="flex flex-col gap-2 border-t pt-3" style={{ borderColor: "var(--line-soft)" }}>
        <span className="text-sm font-semibold">Albums already listed</span>
        {plan.length === 0 ? (
          <p className="m-0 text-xs text-ink-faint">Every listed album already matches these types.</p>
        ) : (
          <>
            <p className="m-0 text-xs text-ink-dim">Applying the types would {plan.join("; ")}.</p>
            <div>
              <Button size="sm" onClick={() => void apply()} disabled={dirty} busy={applying} busyLabel="Applying…"
                title={dirty ? "Save the options first" : undefined}>
                Want matching albums…
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
