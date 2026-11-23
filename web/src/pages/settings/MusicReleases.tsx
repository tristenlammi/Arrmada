import { useEffect, useState } from "react";
import { Section } from "../../components/settings/ui";
import { ReleaseTypePicker, sameTypes } from "../../components/music/releaseRules";
import { musicApi, type MonitorNew, type ReleaseDefaults, type ReleaseTypeChoices, type ReleaseTypes } from "../../lib/musicApi";
import { Button, useToast } from "../../ui";
import { Field } from "../../ui/Field";
import { Select } from "../../ui/Select";

// The library-wide defaults for keeping Music current (MUS-13/14): what happens to an
// artist's new releases, and which kinds of release are listed, for every artist without
// its own choice (Options on the artist page). Like an artist's own options, a change
// never wants or unwants an album by itself. Artist pages show the kinds the types list at
// once (plus anything wanted or holding files); new albums follow from the next refresh.
// Shown only while the Music module is on (its API answers 404 when off).

export function MusicReleasesSettings() {
  const toast = useToast();
  const [saved, setSaved] = useState<ReleaseDefaults | null>(null);
  const [defaults, setDefaults] = useState<ReleaseDefaults | null>(null);
  const [choices, setChoices] = useState<ReleaseTypeChoices | null>(null);
  const [off, setOff] = useState(false);
  const [monitorNew, setMonitorNew] = useState<MonitorNew>("all");
  const [types, setTypes] = useState<ReleaseTypes | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let live = true;
    musicApi
      .releaseDefaults()
      .then((r) => {
        if (!live) return;
        setSaved(r.settings);
        setDefaults(r.defaults);
        setChoices(r.choices);
        setMonitorNew(r.settings.monitor_new);
        setTypes(r.settings.release_types);
      })
      .catch(() => live && setOff(true)); // Music is off: no card
    return () => {
      live = false;
    };
  }, []);

  if (off || !saved || !defaults || !choices || !types) return null;

  const dirty = monitorNew !== saved.monitor_new || !sameTypes(types, saved.release_types);
  const isDefault = monitorNew === defaults.monitor_new && sameTypes(types, defaults.release_types);
  const save = async (next: ReleaseDefaults) => {
    setSaving(true);
    try {
      const r = await musicApi.setReleaseDefaults(next);
      setSaved(r.settings);
      setMonitorNew(r.settings.monitor_new);
      setTypes(r.settings.release_types);
      toast("Saved. Artist pages follow it now; new albums from each artist's next refresh.");
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setSaving(false);
    }
  };

  return (
    <Section
      id="music-releases"
      title="Music: new releases"
      subtitle="Followed artists are refreshed from MusicBrainz about once a day. These are the defaults for every artist without its own choice (Options on the artist page)."
    >
      <Field
        label="New releases"
        hint="An album MusicBrainz newly lists for a monitored artist. Older music it only now lists, and anything a change of types brings in, is listed but never wanted on its own; Music → New releases shows both."
      >
        <Select value={monitorNew} onChange={(e) => setMonitorNew(e.target.value as MonitorNew)} className="w-full max-w-[280px] px-3 py-2 text-base">
          <option value="all">Want them</option>
          <option value="none">Just list them</option>
        </Select>
      </Field>
      <ReleaseTypePicker idPrefix="music-defaults" value={types} choices={choices} onChange={setTypes} disabled={saving} />
      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" variant="primary" onClick={() => void save({ monitor_new: monitorNew, release_types: types })} disabled={!dirty} busy={saving} busyLabel="Saving…">
          Save
        </Button>
        <Button size="sm" onClick={() => void save(defaults)} disabled={isDefault || saving}>
          Reset to defaults
        </Button>
        <span className="text-xs text-ink-faint">
          Artist pages show only these kinds, plus albums that are wanted or have files. Nothing is wanted or unwanted by itself; an artist's Options can apply the types to its wanted albums.
        </span>
      </div>
    </Section>
  );
}
