import { Field, Preview, Section, Toggle, TokenList, input, inputStyle } from "../../components/settings/ui";
import { SaveBar, useLoadedSettings } from "../../lib/useSettings";

// Sample release used for the live naming preview.
const SAMPLE = {
  title: "Blade Runner 2049",
  year: "2017",
  quality: "2160p BluRay",
  resolution: "2160p",
  source: "BluRay",
  edition: "Director's Cut",
  codec: "x265",
  group: "FraMeSToR",
};

const TOKENS = ["title", "year", "quality", "resolution", "source", "edition", "codec", "group"];

// Sample series episode for the series naming preview.
const SERIES_SAMPLE = {
  title: "The Bear",
  year: "2022",
  season: "4",
  season00: "04",
  episode: "S04E01",
  episodetitle: "Tomorrow",
  quality: "1080p WEB-DL",
  resolution: "1080p",
  source: "WEB-DL",
  codec: "x264",
  group: "NTb",
};
const SERIES_TOKENS = ["title", "year", "season", "season00", "episode", "episodetitle", "quality", "resolution", "source", "codec", "group"];

// renderWith mirrors the backend renderName tidy-up: substitute tokens, drop empty
// bracket pairs and stranded "- -" separators, strip illegal filename chars.
function renderWith(format: string, sample: Record<string, string>): string {
  let out = format;
  for (const [k, v] of Object.entries(sample)) out = out.split(`{${k}}`).join(v);
  out = out.replace(/\(\)/g, "").replace(/\[\]/g, "").replace(/\s+/g, " ");
  while (out.includes("- -")) out = out.replace("- -", "-");
  out = out.replace(/^[\s-]+|[\s-]+$/g, "");
  return out.replace(/[<>:"/\\|?*]/g, "");
}
const render = (format: string) => renderWith(format, SAMPLE);
const renderSeries = (format: string) => renderWith(format, SERIES_SAMPLE);

// Settings → Naming & metadata (the old Media tab): how imported files are named, and the
// sidecars written next to them.
export function NamingSettings() {
  const { s, patch } = useLoadedSettings();
  return (
    <div className="flex flex-col gap-6">
      <Section id="movie-naming" title="Movie naming" subtitle="How imported movie files are named. Tokens are replaced per release.">
        <Field label="Folder name">
          <input value={s.naming_movie_folder} onChange={(e) => patch({ naming_movie_folder: e.target.value })} className={input} style={inputStyle} />
          <Preview>{render(s.naming_movie_folder)}</Preview>
        </Field>
        <Field label="File name">
          <input value={s.naming_movie_file} onChange={(e) => patch({ naming_movie_file: e.target.value })} className={input} style={inputStyle} />
          <Preview>{render(s.naming_movie_file)}.mkv</Preview>
        </Field>
        <TokenList tokens={TOKENS} />
      </Section>
      <Section id="series-naming" title="Series naming" subtitle="How imported episodes are named: the show folder holds season folders, which hold episode files.">
        <Field label="Series folder">
          <input value={s.naming_series_folder} onChange={(e) => patch({ naming_series_folder: e.target.value })} className={input} style={inputStyle} />
          <Preview>{renderSeries(s.naming_series_folder)}</Preview>
        </Field>
        <Field label="Season folder">
          <input value={s.naming_series_season} onChange={(e) => patch({ naming_series_season: e.target.value })} className={input} style={inputStyle} />
          <Preview>{renderSeries(s.naming_series_season)}</Preview>
        </Field>
        <Field label="Episode file">
          <input value={s.naming_series_episode} onChange={(e) => patch({ naming_series_episode: e.target.value })} className={input} style={inputStyle} />
          <Preview>{renderSeries(s.naming_series_episode)}.mkv</Preview>
        </Field>
        <p className="text-[11px] text-ink-faint">
          <code className="font-mono">{"{episode}"}</code> is the SxxExx tag (a range for double episodes). Use <code className="font-mono">{"{season00}"}</code> for a zero-padded season number. Season 0 is always “Specials”.
        </p>
        <TokenList tokens={SERIES_TOKENS} />
      </Section>
      <Section id="metadata" title="Metadata" subtitle="Written into each movie folder for Plex, Jellyfin, Emby and Kodi.">
        <Toggle label="Write movie.nfo" hint="A metadata sidecar with title, plot, ids, ratings." checked={s.write_nfo} onChange={(v) => patch({ write_nfo: v })} />
        <Toggle label="Download artwork" hint="Save poster.jpg and fanart.jpg next to the movie." checked={s.download_artwork} onChange={(v) => patch({ download_artwork: v })} />
      </Section>
      <SaveBar />
    </div>
  );
}
