import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Note } from "../../components/settings/ui";
import { setJellyfinSlotKind } from "../../components/brand/servers";
import { jellyfinApi, type JellyfinConfig, type JellyfinServerKind, type JellyfinTestResult } from "../../lib/jellyfinApi";
import { lanOnly } from "../../lib/lanAddress";
import { secretMoved } from "../../lib/sameEndpoint";
import { LINKS } from "../../lib/links";
import { setQueryData, useQuery } from "../../lib/query";
import { useUnsavedGuard } from "../../lib/useUnsavedGuard";
import { Button, StatusChip, useConfirm, useToast } from "../../ui";
import { Field } from "../../ui/Field";
import { Input } from "../../ui/Input";
import { Select } from "../../ui/Select";

// JellyfinConnect points Arrmada at a Jellyfin server: its URL, an API key made in
// Jellyfin, and the public address Watch links use. It opens inside the Jellyfin card on
// Settings → Connections and reads and writes the connection through the API on its own.
// The key is a credential: it's typed here, saved with Save (not auto-saved) and never
// comes back — the form only says a key is set, and a blank key field keeps it.
//
// The same form connects an Emby server instead (Emby is what Jellyfin grew out of, and
// speaks nearly the same API): the Server choice at the top, and every word follows it.
//
// testFirst (the setup wizard): Save tests what's typed first and only saves an address or
// key Jellyfin turned down if the admin says so. inWizard leaves out the link to Settings
// → Media servers, which the wizard covers itself.

const JELLYFIN_KEY = "jellyfin";

const KINDS: Record<JellyfinServerKind, { label: string; keyHint: string; url: string; publicUrl: string }> = {
  jellyfin: {
    label: "Jellyfin",
    keyHint: "Make one in Jellyfin: Dashboard → API Keys → +. It's never shown again once saved.",
    url: "http://jellyfin:8096",
    publicUrl: "https://jellyfin.example.com",
  },
  emby: {
    label: "Emby",
    keyHint: "Make one in Emby: Dashboard → Advanced → API Keys → New API Key. It's never shown again once saved.",
    url: "http://emby:8096",
    publicUrl: "https://emby.example.com",
  },
};

export function JellyfinConnect({ onChanged, testFirst, inWizard }: { onChanged?: () => void; testFirst?: boolean; inWizard?: boolean }) {
  const q = useQuery(JELLYFIN_KEY, jellyfinApi.get, { staleMs: 0 });
  return (
    <div className="mt-2 flex flex-col gap-4 border-t pt-3" style={{ borderColor: "var(--line)" }}>
      {q.data ? (
        <JellyfinForm cfg={q.data} onChanged={onChanged} testFirst={testFirst} inWizard={inWizard} />
      ) : q.error ? (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span role="alert" style={{ color: "var(--reject-text)" }}>Couldn't load the Jellyfin connection: {q.error.message}</span>
          <Button size="sm" onClick={() => void q.refetch()} busy={q.loading} busyLabel="Retrying…">Retry</Button>
        </div>
      ) : (
        <p className="m-0 text-sm text-ink-dim">Loading…</p>
      )}
    </div>
  );
}

function JellyfinForm({ cfg, onChanged, testFirst, inWizard }: { cfg: JellyfinConfig; onChanged?: () => void; testFirst?: boolean; inWizard?: boolean }) {
  const toast = useToast();
  const confirm = useConfirm();
  const savedKind: JellyfinServerKind = cfg.server_kind === "emby" ? "emby" : "jellyfin";
  const [kind, setKind] = useState<JellyfinServerKind>(savedKind);
  const [url, setUrl] = useState(cfg.url);
  const [key, setKey] = useState("");
  // The public address follows the URL until it's typed in itself.
  const [ownPublic, setOwnPublic] = useState<string | null>(cfg.public_url || null);
  const publicUrl = ownPublic ?? url;
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const [test, setTest] = useState<JellyfinTestResult | null>(null);
  const k = KINDS[kind];
  // The kind goes up only when it says something: Emby, or a change. A blank kind keeps
  // the saved one, so a Jellyfin connection saves exactly as it always has.
  const kindField = kind === "emby" || kind !== savedKind ? { server_kind: kind } : {};
  const name = k.label;

  const savedPublic = cfg.public_url || cfg.url;
  const dirty = kind !== savedKind || url.trim() !== cfg.url || key.trim() !== "" || publicUrl.trim() !== savedPublic;
  const markClean = useUnsavedGuard(dirty);
  // The saved key is only sent to the address it was saved for (the server refuses a new
  // address with the key left blank), so a changed address asks for it again.
  const keyMoved = secretMoved({ saved: cfg.key_set, typed: key, url, savedUrl: cfg.url });
  const needsKey = (!cfg.key_set || keyMoved) && !key.trim();
  const homeOnly = lanOnly(publicUrl);

  const save = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!url.trim() || needsKey) return;
    if (testFirst && (kind !== savedKind || url.trim() !== cfg.url || key.trim())) {
      setBusy("test");
      setTest(null);
      const r = await jellyfinApi.test({ ...kindField, url: url.trim(), api_key: key.trim() || undefined }).catch((err: Error) => ({ ok: false, detail: err.message }) as JellyfinTestResult);
      setTest(r);
      setBusy(null);
      if (!r.ok && !(await confirm({
        title: "Save it anyway?",
        body: `${name} didn't answer as expected: ${r.detail || "no answer"}. Watch links, library updates and sign-in won't work until it does.`,
        confirmLabel: "Save anyway",
        cancelLabel: "Keep editing",
      }))) return;
    }
    setBusy("save");
    try {
      const next = await jellyfinApi.save({ ...kindField, url: url.trim(), public_url: publicUrl.trim(), api_key: key.trim() || undefined });
      setQueryData(JELLYFIN_KEY, next);
      setJellyfinSlotKind(next.server_kind);
      setKey("");
      setUrl(next.url);
      setOwnPublic(next.public_url || null);
      markClean();
      onChanged?.();
      toast(`${name} connection saved`, { tone: "good" });
    } catch (err) {
      toast((err as Error).message, { tone: "error" });
    } finally {
      setBusy(null);
    }
  };

  const runTest = async () => {
    setBusy("test");
    setTest(null);
    try {
      setTest(await jellyfinApi.test({ ...kindField, url: url.trim() || undefined, api_key: key.trim() || undefined }));
    } catch (err) {
      setTest({ ok: false, detail: (err as Error).message });
    } finally {
      setBusy(null);
    }
  };

  return (
    <form onSubmit={save} aria-label={`${name} connection`} className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        {cfg.key_set ? <StatusChip tone="good">Key set</StatusChip> : <StatusChip tone="faint">No key yet</StatusChip>}
        {cfg.server_name && (
          <span className="text-ink-dim">{cfg.server_name}{cfg.version ? ` · ${KINDS[savedKind].label} ${cfg.version}` : ""}</span>
        )}
      </div>

      <Field label="Server" hint="Emby works here too: it's the server Jellyfin grew out of, and speaks nearly the same language.">
        <Select value={kind} onChange={(e) => { setKind(e.target.value === "emby" ? "emby" : "jellyfin"); setTest(null); }}>
          <option value="jellyfin">Jellyfin</option>
          <option value="emby">Emby</option>
        </Select>
      </Field>
      {kind === "emby" && (
        <Note tone="info">
          Emby has no Quick Connect, so people sign in with Emby using their username and password. Everything else
          (Watch links, library updates, "ready" checks, watch monitoring and sign-in) works as it does with Jellyfin.
        </Note>
      )}

      <Field label="Server URL" hint={`As Arrmada reaches it, for example http://192.168.1.10:8096 or ${k.url}.`}>
        <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.1.10:8096" autoComplete="off" spellCheck={false} />
      </Field>

      <Field
        label="API key"
        hint={keyMoved
          ? <span style={{ color: "var(--accent-text)" }}>New address: enter its API key. The saved key is only sent to the address it was saved for.</span>
          : k.keyHint}
      >
        <Input
          type="password"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder={keyMoved ? "Enter the key for the new address" : cfg.key_set ? "Key set — leave blank to keep it" : "Paste the API key"}
          autoComplete="new-password"
        />
      </Field>

      <div className="flex flex-col gap-2">
        <Field label="Public address for Watch links" hint={`Where people open ${name} from outside your home. Starts as the server URL.`}>
          <Input value={publicUrl} onChange={(e) => setOwnPublic(e.target.value)} placeholder={k.publicUrl} autoComplete="off" spellCheck={false} />
        </Field>
        {homeOnly && (
          <Note tone="warn">
            This address only works on your home network, so Watch links won't open on a phone away from home.
            Use the address you reach {name} on from outside, like {k.publicUrl}.
          </Note>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={runTest} busy={busy === "test"} busyLabel="Testing…" disabled={busy !== null || !url.trim()}>Test</Button>
        <Button type="submit" variant="primary" busy={busy === "save"} busyLabel="Saving…" disabled={busy !== null || !url.trim() || needsKey}>Save</Button>
        {needsKey && url.trim() && !keyMoved && <span className="text-xs text-ink-faint">Add an API key to save.</span>}
      </div>

      {test && (
        <div
          role="status"
          className="break-words rounded-lg p-3 text-sm font-semibold"
          style={{
            border: `1px solid ${test.ok ? "var(--good)" : "var(--reject)"}`,
            background: test.ok ? "var(--good-soft)" : "var(--reject-soft)",
            color: test.ok ? "var(--good-text)" : "var(--reject-text)",
          }}
        >
          {test.ok
            ? `✓ Connected${test.server_name ? ` to ${test.server_name}` : ""}${test.version ? ` · ${name} ${test.version}` : ""}`
            : `✕ ${test.detail || `Couldn't reach ${name}`}`}
        </div>
      )}
      {!inWizard && (
        <p className="m-0 text-xs text-ink-faint">
          Watch monitoring, library updates, who may sign in with {name} and the accounts linked to it: <Link to={LINKS.jellyfinServer} className="font-semibold" style={{ color: "var(--accent-text)" }}>Settings → Media servers →</Link>
        </p>
      )}
    </form>
  );
}
