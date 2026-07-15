import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type Status, type Health, type SystemHealth } from "../lib/api";
import { useLive } from "../lib/useLive";

export function Dashboard() {
  const [status, setStatus] = useState<Status | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [system, setSystem] = useState<SystemHealth | null>(null);
  const [error, setError] = useState<string | null>(null);
  const { connected, last } = useLive();

  useEffect(() => {
    Promise.all([api.status(), api.health()])
      .then(([s, h]) => {
        setStatus(s);
        setHealth(h);
      })
      .catch((e: Error) => setError(e.message));
    api.systemHealth().then(setSystem).catch(() => {});
  }, []);

  const modules = status?.modules ?? [];
  const dbOK = health?.checks?.database === "ok";

  return (
    <>
      <PageHeader title="Dashboard" crumb="Overview" />
      <div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
        {system && system.warnings.length > 0 && (
          <div className="mb-4 flex flex-col gap-2">
            {system.warnings.map((wrn, i) => (
              <div
                key={i}
                className="flex items-center gap-2.5 rounded-lg px-3.5 py-2.5 text-[12.5px]"
                style={{
                  background: wrn.level === "error" ? "var(--reject-soft)" : "var(--avoid-soft, var(--panel-2))",
                  border: `1px solid ${wrn.level === "error" ? "var(--reject)" : "var(--avoid, var(--line))"}`,
                  color: wrn.level === "error" ? "var(--reject)" : "var(--ink-dim)",
                }}
              >
                <span>{wrn.level === "error" ? "⛔" : "⚠️"}</span>
                <span>{wrn.message}</span>
              </div>
            ))}
          </div>
        )}
        <div className="mb-4 flex items-center gap-2">
          <span
            className="inline-flex items-center gap-2 rounded-full px-3 py-1.5 font-mono text-[11px]"
            style={{
              background: connected ? "var(--good-soft)" : "var(--reject-soft)",
              color: connected ? "var(--good)" : "var(--reject)",
              border: `1px solid ${connected ? "var(--good)" : "var(--reject)"}`,
            }}
          >
            <span
              className="h-1.5 w-1.5 rounded-full"
              style={{ background: "currentColor" }}
            />
            {connected ? "Realtime connected" : "Realtime offline"}
          </span>
          {last?.topic === "server.heartbeat" &&
            typeof (last.data as { uptime_seconds?: number })?.uptime_seconds === "number" && (
              <span className="font-mono text-[11px] text-ink-faint">
                last heartbeat @ {(last.data as { uptime_seconds: number }).uptime_seconds}s uptime
              </span>
            )}
        </div>
        <Card>
          <div className="mb-3 flex items-center gap-2.5">
            <span
              className="inline-block h-2 w-2 rounded-full"
              style={{ background: error ? "var(--reject)" : "var(--good)" }}
            />
            <h2 className="m-0 text-sm font-bold">
              {error ? "Backend unreachable" : "Foundation online"}
            </h2>
          </div>
          {error ? (
            <p className="m-0 text-[12.5px] text-ink-dim">Could not reach the API: {error}</p>
          ) : status ? (
            <dl className="grid grid-cols-2 gap-x-6 gap-y-3 font-mono text-[12px] text-ink-dim sm:grid-cols-4">
              <Stat k="Version" v={status.version} />
              <Stat k="Uptime" v={`${status.uptime_seconds}s`} />
              <Stat k="Database" v={dbOK ? "ok" : "down"} tone={dbOK ? "good" : "bad"} />
              <Stat k="Auth" v={status.auth_enabled ? "enabled" : "disabled"} />
            </dl>
          ) : (
            <p className="m-0 text-[12.5px] text-ink-dim">Connecting…</p>
          )}
        </Card>

        <SectionLabel>Modules · planned</SectionLabel>
        <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3">
          {modules.map((m) => (
            <div
              key={m.id}
              className="rounded-[11px] p-3.5"
              style={{ background: "var(--panel)", border: "1px solid var(--line)" }}
            >
              <div className="text-[13px] font-semibold">{m.name}</div>
              <div
                className="mt-1.5 inline-block rounded-md px-2 py-0.5 font-mono text-[10px]"
                style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}
              >
                {m.status}
              </div>
            </div>
          ))}
        </div>

        <p className="mt-8 text-[11.5px] text-ink-faint">
          M0 foundation — the app shell, module registry, database, scheduler, and API are live. Real
          modules land from M2 (Movies) onward.
        </p>
      </div>
    </>
  );
}

function Card({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      {children}
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-3 mt-6 font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-ink-faint">
      {children}
    </div>
  );
}

function Stat({ k, v, tone }: { k: string; v: string; tone?: "good" | "bad" }) {
  const color = tone === "good" ? "var(--good)" : tone === "bad" ? "var(--reject)" : "var(--ink)";
  return (
    <div>
      <dt className="text-[9px] uppercase tracking-[0.1em] text-ink-faint">{k}</dt>
      <dd className="m-0 mt-0.5" style={{ color }}>
        {v}
      </dd>
    </div>
  );
}
