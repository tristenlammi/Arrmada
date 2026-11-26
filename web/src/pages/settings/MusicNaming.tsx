import { useEffect, useState } from "react";
import { MusicRenameModal } from "../../components/music/MusicRenameModal";
import { AutoText, AutoToggle } from "../../components/settings/auto";
import { Section, TokenList } from "../../components/settings/ui";
import { musicNamingApi, type MusicNaming, type MusicNamingPreset, type MusicNamingPreview } from "../../lib/musicNamingApi";
import { useLoadedSettings, useSetting } from "../../lib/useSettings";
import { Button, useConfirm, useToast } from "../../ui";

// A blank template would quietly fall back to the default, so say so instead of saving it.
const notBlank = (v: string) => (v.trim() === "" ? "A name template can't be empty. Esc puts the saved one back." : null);

// Shown until the presets load (and if they can't): the server's token list is the same.
const FALLBACK_TOKENS = ["Album Artist", "Album Artist Sort", "Album Artist First Letter", "Album Title", "Release Year", "Original Year", "Album Type", "Edition", "Media", "Disc", "Disc Count", "Disc Title", "Disc Prefix", "Track", "Track Title", "Track Artist", "Quality", "Codec", "Bit Depth", "Sample Rate", "Album MBID", "Release MBID"];

const FIELD_NAME = { folder: "Album folder", track: "Track file", compilation: "Compilation track file" } as const;

// usePreview renders the sample albums with the templates as typed, a moment after the
// last keystroke. The server renders them, so the preview is exactly what an import does.
function usePreview(folder: string, track: string, compilation: string): MusicNamingPreview | null {
  const [preview, setPreview] = useState<MusicNamingPreview | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      musicNamingApi.preview({ folder, track, compilation }, ctl.signal)
        .then(setPreview)
        .catch(() => { /* cancelled by the next keystroke, or offline: keep the last preview */ });
    }, 250);
    return () => { clearTimeout(timer); ctl.abort(); };
  }, [folder, track, compilation]);
  return preview;
}

function Samples({ preview }: { preview: MusicNamingPreview | null }) {
  if (!preview) return <div className="text-xs text-ink-faint">Rendering the examples…</div>;
  if (preview.error) {
    return (
      <div className="text-xs" style={{ color: "var(--reject-text)" }}>
        {FIELD_NAME[preview.error.field]}: {preview.error.message}
      </div>
    );
  }
  return (
    <div role="group" aria-label="Music examples" className="flex flex-col gap-2 rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      {preview.samples.map((s) => (
        <div key={s.label}>
          <div className="text-xs text-ink-faint">{s.label}</div>
          {s.paths.map((p) => (
            <div key={p} className="break-all font-mono text-xs" style={{ color: "var(--accent-text)" }}>{p}</div>
          ))}
        </div>
      ))}
    </div>
  );
}

const same = (a: MusicNaming, b: MusicNaming) => a.folder === b.folder && a.track === b.track && a.compilation === b.compilation;

// MusicNamingSettings: how imported music is named and tagged (MUS-33, MUS-34, MUS-36) —
// the house tags switch, the folder and file templates with a live preview and presets,
// the extras brought along, and the rename that applies a new scheme to the albums already
// in the library (never automatic). Shown while the Music module is on.
export function MusicNamingSettings() {
  const { s } = useLoadedSettings();
  if (!s.music_enabled) return null;
  return <MusicNamingCard />;
}

function MusicNamingCard() {
  const writeTags = useSetting("music_write_tags");
  const transcode = useSetting("music_transcode");
  const transcodeQuality = useSetting("music_transcode_quality");
  const transcodeLosslessOnly = useSetting("music_transcode_lossless_only");
  const folder = useSetting("naming_music_folder", { validate: notBlank });
  const track = useSetting("naming_music_track", { validate: notBlank });
  const compilation = useSetting("naming_music_compilation", { validate: notBlank });
  const extras = useSetting("music_extras");
  const confirm = useConfirm();
  const toast = useToast();
  const [presets, setPresets] = useState<MusicNamingPreset[]>([]);
  const [tokens, setTokens] = useState<string[]>(FALLBACK_TOKENS);
  const [applying, setApplying] = useState<string | null>(null);
  const [organizing, setOrganizing] = useState(false);

  useEffect(() => {
    let alive = true;
    musicNamingApi.presets()
      .then((r) => { if (alive) { setPresets(r.presets); setTokens(r.tokens); } })
      .catch(() => { /* no presets: the templates still work by hand */ });
    return () => { alive = false; };
  }, []);

  const preview = usePreview(folder.value, track.value, compilation.value);
  const { s } = useLoadedSettings();
  const saved = { folder: s.naming_music_folder, track: s.naming_music_track, compilation: s.naming_music_compilation };

  const applyPreset = async (p: MusicNamingPreset) => {
    const ok = await confirm({
      title: `Use ${p.label}?`,
      body: "The three music templates are replaced. Albums already in your library keep their names until you rename them.",
      confirmLabel: "Use it",
    });
    if (!ok) return;
    setApplying(p.id);
    try {
      const done = await Promise.all([folder.commit(p.naming.folder), track.commit(p.naming.track), compilation.commit(p.naming.compilation)]);
      if (done.every(Boolean)) toast(`Music naming set to ${p.label}.`);
    } finally {
      setApplying(null);
    }
  };

  return (
    <Section id="music-naming" title="Music naming & tags" subtitle="How imported albums are tagged and named. Put / between folders.">
      <AutoToggle
        label="Write house tags"
        hint="One tag style that Plex, Jellyfin and Navidrome all read: every MusicBrainz id, each credited artist, disc and track totals, the album cover. Tags go into a copy, so a download that's still seeding is never touched. Off keeps files exactly as downloaded (hardlinked, untouched)."
        save={writeTags}
      />

      <div className="flex flex-col gap-2 rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
        <AutoToggle
          label="Transcode music on import"
          hint="Convert imported audio to MP3 in a staging copy before placing it in the library. The download/source files are never touched, so seeding is safe. Useful when you want broad indexer coverage but smaller, widely compatible files."
          save={transcode}
        />
        {transcode.value && (
          <>
            <label className="flex flex-col gap-1 text-sm">
              <span className="text-ink-faint">MP3 quality</span>
              <select
                className="w-full max-w-xs rounded-md border px-2 py-1.5 text-sm"
                style={{ background: "var(--panel)", borderColor: "var(--line)" }}
                value={transcodeQuality.value}
                onChange={(e) => void transcodeQuality.commit(e.target.value as "v0" | "v2" | "320" | "256" | "192")}
              >
                <option value="v0">V0 (recommended, high quality VBR)</option>
                <option value="320">320 kbps CBR</option>
                <option value="v2">V2 (smaller VBR)</option>
                <option value="256">256 kbps CBR</option>
                <option value="192">192 kbps CBR</option>
              </select>
            </label>
            <AutoToggle
              label="Only transcode lossless sources"
              hint="When on, existing lossy files (MP3/AAC/OGG/Opus) are kept as-is to avoid re-encoding quality loss. Turn off to force MP3 output for all imported music."
              save={transcodeLosslessOnly}
            />
          </>
        )}
      </div>

      {presets.length > 0 && (
        <div className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-2">
            {presets.map((p) => {
              const inUse = same(saved, p.naming);
              return (
                <Button key={p.id} size="sm" variant={inUse ? "secondary" : "primary"} onClick={() => void applyPreset(p)}
                  busy={applying === p.id} busyLabel="Saving…" disabled={inUse || applying !== null} title={p.description}>
                  {inUse ? `Using ${p.label}` : p.label}
                </Button>
              );
            })}
          </div>
          <span className="text-xs text-ink-faint">A preset fills in every template below; adjust any of them after.</span>
        </div>
      )}

      <AutoText label="Album folder" save={folder} />
      <AutoText label="Track file" save={track}>
        <span className="text-xs text-ink-faint">A / here makes a folder inside the album folder, one per disc for example.</span>
      </AutoText>
      <AutoText label="Compilation track file" save={compilation}>
        <span className="text-xs text-ink-faint">For a Various Artists album, where each file should say whose track it is.</span>
      </AutoText>
      <Samples preview={preview} />

      <TokenList tokens={tokens} />
      <p className="m-0 text-xs text-ink-faint">
        <code className="font-mono">{"{Disc Prefix}"}</code> is “1-” only on an album with more than one disc, so a single disc’s files are “04 - Title”. Wrap a part in <code className="font-mono">{"< >"}</code> to write it only when its tokens have values — <code className="font-mono">{"{Album Title}< [{Edition}]>"}</code> — and pad numbers with zeros, <code className="font-mono">{"{Track:00}"}</code>. Every album gets a folder of its own, so the last folder needs <code className="font-mono">{"{Album Title}"}</code>, and a file name needs <code className="font-mono">{"{Track}"}</code>.
      </p>

      <AutoText label="Extras brought along" save={extras}>
        <span className="text-xs text-ink-faint">File types copied in beside an album (hardlinked, never overwriting anything): artwork, booklets, lyrics, rip logs. Separate them with spaces; leave it empty to bring none.</span>
      </AutoText>

      <div className="flex flex-wrap items-center gap-2.5 text-sm text-ink-faint">
        <Button size="sm" variant="secondary" onClick={() => setOrganizing(true)}>Preview music rename…</Button>
        <span>Albums already in your library keep their names until you rename them.</span>
        {organizing && <MusicRenameModal onClose={() => setOrganizing(false)} />}
      </div>
    </Section>
  );
}
