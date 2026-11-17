import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";

export type SwitchSize = "md" | "sm";

export interface SwitchProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "onChange" | "role" | "aria-checked" | "children"> {
  checked: boolean;
  onChange: (next: boolean) => void;
  /**
   * The switch's name for a screen reader when nothing visible names it. Leave it out
   * when `children` (text inside the switch) or a <label htmlFor> / aria-labelledby does.
   */
  label?: string;
  /** Visible text beside the track, inside the button, so it is clickable too. */
  children?: ReactNode;
  /** md is the Settings switch; sm the slimmer one on title pages and editors. */
  size?: SwitchSize;
}

// Switch is the one on/off control: a real button with role=switch and aria-checked, so
// a screen reader says "on"/"off" and Space and Enter flip it like any button. Its name
// comes from `label`, from the text inside it, or from a <label htmlFor={id}> outside it
// (clicking that label flips it too).
export const Switch = forwardRef<HTMLButtonElement, SwitchProps>(function Switch(
  { checked, onChange, label, children, size = "md", className, type = "button", onClick, ...rest },
  ref,
) {
  const track = size === "md" ? <MdTrack on={checked} /> : <SmTrack on={checked} />;
  return (
    <button
      ref={ref}
      type={type}
      role="switch"
      aria-checked={checked}
      aria-label={children ? undefined : label}
      onClick={(e) => { onClick?.(e); if (!e.defaultPrevented) onChange(!checked); }}
      className={children ? `inline-flex items-center gap-2 text-base font-semibold disabled:opacity-50 ${className ?? ""}` : `inline-flex flex-none rounded-full disabled:opacity-60 ${className ?? ""}`}
      {...rest}
    >
      {track}
      {children && <span style={{ color: checked ? "var(--ink)" : "var(--ink-dim)" }}>{children}</span>}
    </button>
  );
});

function MdTrack({ on }: { on: boolean }) {
  return (
    <span
      className="relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors"
      style={{ background: on ? "var(--accent)" : "var(--panel-2)", border: "1px solid var(--line)" }}
    >
      <span className="inline-block h-4 w-4 rounded-full bg-white transition-transform" style={{ transform: on ? "translateX(22px)" : "translateX(3px)" }} />
    </span>
  );
}

function SmTrack({ on }: { on: boolean }) {
  return (
    <span className="relative inline-block h-[22px] w-[38px] flex-none rounded-full transition-colors" style={{ background: on ? "var(--accent)" : "var(--line)" }}>
      <span className="absolute top-[3px] h-[16px] w-[16px] rounded-full bg-white transition-all" style={{ left: on ? "19px" : "3px" }} />
    </span>
  );
}
