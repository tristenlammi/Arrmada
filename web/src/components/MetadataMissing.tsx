import { Link } from "react-router-dom";
import { useMe, isAdmin, isStaff } from "../lib/me";
import { LINKS } from "../lib/links";

// MetadataMissing is the one "no TMDB key" message, shown once per page instead of once per
// failing request. What it says depends on who is looking: only an admin can enter the key
// (and it takes effect straight away), a manager can ask one, and a requester can do nothing
// about it, so they get a plain sentence with no mention of Settings or keys.
export function MetadataMissing({ variant }: { variant: "banner" | "empty" }) {
  const { user } = useMe();
  const text = isAdmin(user) ? (
    <>
      <b>Movie and TV metadata isn't set up.</b> Add a free TMDB key in{" "}
      <Link to={LINKS.apiKeys} className="font-semibold underline" style={{ color: "inherit" }}>Settings → System → API keys</Link>
      {" "}— it takes effect straight away, no restart.
    </>
  ) : isStaff(user) ? (
    <><b>Movie and TV metadata isn't set up.</b> Ask an admin to add a TMDB key (Settings → System → API keys).</>
  ) : (
    <>Movie and TV browsing isn't set up on this server yet — ask the person who runs it.</>
  );

  if (variant === "banner") {
    return (
      <div className="mb-4 rounded-lg p-3.5 text-[12.5px]" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
        {text}
      </div>
    );
  }
  return (
    <div className="rounded-xl px-5 py-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px dashed var(--line)", background: "var(--panel)" }}>
      <p className="mx-auto m-0 max-w-[52ch] leading-relaxed">{text}</p>
    </div>
  );
}
