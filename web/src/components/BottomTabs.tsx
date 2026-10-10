import { useEffect, useState } from "react";
import { NavLink } from "react-router-dom";
import { ShellIcon } from "./ShellIcons";
import type { RequesterNavItem } from "../lib/requesterNav";

// Inputs that bring up a phone's on-screen keyboard.
const KEYBOARD_INPUTS = new Set(["text", "search", "email", "url", "tel", "password", "number"]);
function opensKeyboard(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  if (el instanceof HTMLTextAreaElement || el.isContentEditable) return true;
  return el instanceof HTMLInputElement && KEYBOARD_INPUTS.has(el.type);
}

// BottomTabs is the requester's phone navigation: a bar pinned to the bottom below 640px,
// clear of the iPhone home indicator. Its height is the --tabbar-h the layout reserves
// (index.css), so pages, toasts and sheets all know to stay above it.
//
// It steps aside while someone types: a fixed bar would otherwise ride up on top of the
// keyboard and cover what they're typing into.
export function BottomTabs({ items }: { items: RequesterNavItem[] }) {
  const [typing, setTyping] = useState(false);
  useEffect(() => {
    const onIn = (e: FocusEvent) => setTyping(opensKeyboard(e.target));
    const onOut = (e: FocusEvent) => setTyping(opensKeyboard(e.relatedTarget));
    document.addEventListener("focusin", onIn);
    document.addEventListener("focusout", onOut);
    return () => {
      document.removeEventListener("focusin", onIn);
      document.removeEventListener("focusout", onOut);
    };
  }, []);

  return (
    <nav
      aria-label="Primary"
      className={`fixed inset-x-0 bottom-0 z-tabbar sm:hidden ${typing ? "hidden" : "flex"}`}
      style={{ background: "var(--sidebar)", borderTop: "1px solid var(--line)", paddingBottom: "env(safe-area-inset-bottom, 0px)" }}
    >
      {items.map((t) => (
        <NavLink
          key={t.key}
          to={t.to}
          className="flex h-14 min-w-0 flex-1 flex-col items-center justify-center gap-0.5 text-[10.5px] font-semibold"
          style={({ isActive }) => ({ color: isActive ? "var(--accent)" : "var(--ink-dim)" })}
        >
          {({ isActive }) => (
            <>
              <span className="grid h-7 w-12 place-items-center rounded-full" style={{ background: isActive ? "var(--accent-soft)" : "transparent" }}>
                <ShellIcon name={t.icon} />
              </span>
              <span className="max-w-full truncate px-0.5">{t.tabLabel}</span>
            </>
          )}
        </NavLink>
      ))}
    </nav>
  );
}
