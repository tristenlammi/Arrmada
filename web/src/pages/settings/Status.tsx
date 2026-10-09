import { Link } from "react-router-dom";
import { Section } from "../../components/settings/ui";

// PLACEHOLDER — Settings → Status. The System → Status page (health checks) replaces this
// file with the real StatusSection; the hub already routes /settings/status here and lists
// it in the rail, so only this component changes. Until then it points at what exists.
export function StatusSection() {
  return (
    <Section id="status" title="Status" subtitle="How Arrmada is doing right now.">
      <p className="m-0 text-[12px] text-ink-dim">
        Warnings and errors are in <Link to="/logs" style={{ color: "var(--accent)" }}>Logs</Link>; restarts and backups are under <Link to="/settings/system" style={{ color: "var(--accent)" }}>Settings → System</Link>.
      </p>
    </Section>
  );
}
