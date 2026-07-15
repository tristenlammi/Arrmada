import { useEffect, useState } from "react";
import { FleetMark } from "../components/FleetMark";
import { api } from "../lib/api";

// Login is shown when auth is enabled and no session is active. It doubles as the
// first-run setup screen (create the first admin) when the instance has no users yet.
export function Login() {
  const [mode, setMode] = useState<"loading" | "login" | "setup">("loading");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.status().then((s) => setMode(s.needs_setup ? "setup" : "login")).catch(() => setMode("login"));
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      if (mode === "setup") await api.setupAdmin(email.trim(), password);
      else await api.login(email.trim(), password);
      window.location.href = "/discover";
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  };

  const setup = mode === "setup";

  return (
    <div className="grid h-full place-items-center px-5" style={{ background: "var(--bg)" }}>
      <div className="w-full max-w-[380px]">
        <div className="mb-6 flex flex-col items-center gap-2.5 text-center">
          <span className="grid h-12 w-12 place-items-center rounded-2xl" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
            <FleetMark className="h-6 w-6" />
          </span>
          <h1 className="m-0 text-[20px] font-extrabold tracking-[0.14em]">ARRMADA</h1>
          <p className="m-0 text-[12.5px] text-ink-dim">{mode === "loading" ? "…" : setup ? "Create your admin account to get started." : "Sign in to discover and request."}</p>
        </div>

        {mode !== "loading" && (
          <form onSubmit={submit} className="flex flex-col gap-3 rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
            <label className="flex flex-col gap-1.5">
              <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Email</span>
              <input type="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" className="rounded-lg px-3 py-2.5 text-[13px]" style={fieldStyle} />
            </label>
            <label className="flex flex-col gap-1.5">
              <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Password</span>
              <input type="password" required minLength={setup ? 8 : undefined} value={password} onChange={(e) => setPassword(e.target.value)} placeholder={setup ? "8+ characters" : "••••••••"} className="rounded-lg px-3 py-2.5 text-[13px]" style={fieldStyle} />
            </label>
            {error && <div className="text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
            <button type="submit" disabled={busy} className="mt-1 rounded-lg px-4 py-2.5 text-[13px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
              {busy ? "…" : setup ? "Create admin & continue" : "Sign in"}
            </button>
          </form>
        )}
      </div>
    </div>
  );
}

const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
