import { HealthList } from "../../components/system/HealthList";
import { TasksTable } from "../../components/system/TasksTable";

// System → Status: is everything working, and what's running in the background. Health
// lists every check with what's wrong and a link to fix it; Tasks lists every scheduled
// job with its last and next run and Run now. Both refresh themselves while the page is
// open. The ids are deep-link targets (LINKS.status, LINKS.tasks).
export function StatusSection() {
  return (
    <div className="flex flex-col gap-5">
      <section id="health" className="scroll-mt-20 rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
        <h2 className="m-0 text-[14px] font-bold">Health</h2>
        <p className="mb-4 mt-0.5 text-[11.5px] text-ink-faint">
          Arrmada checks its folders, download clients, indexers and connections in the background, every minute or so. Anything wrong shows here and on the Dashboard, with a link to where it's fixed.
        </p>
        <HealthList />
      </section>
      <section id="tasks" className="scroll-mt-20 rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
        <h2 className="m-0 text-[14px] font-bold">Tasks</h2>
        <p className="mb-4 mt-0.5 text-[11.5px] text-ink-faint">
          The jobs Arrmada runs on a schedule: searching for missing titles, importing finished downloads, backups and the rest. Run now starts one straight away; a task that fails three times in a row also shows under Health.
        </p>
        <TasksTable />
      </section>
    </div>
  );
}
