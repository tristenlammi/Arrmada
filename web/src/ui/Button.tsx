import { forwardRef, type ButtonHTMLAttributes, type CSSProperties, type ReactNode } from "react";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type ButtonSize = "sm" | "md";

// The four looks every page already hand-rolls: the terracotta gradient for the one
// main action, a panel button beside it, a bare text button, and the red one.
const LOOK: Record<ButtonVariant, { className: string; style?: CSSProperties }> = {
  primary: { className: "bg-accent-grad text-accent-ink" },
  secondary: { className: "", style: { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } },
  ghost: { className: "", style: { color: "var(--ink-dim)" } },
  danger: { className: "", style: { background: "var(--reject)", color: "#fff" } },
};
const SIZE: Record<ButtonSize, string> = {
  sm: "rounded-md px-2.5 py-1 text-[11.5px]",
  md: "rounded-lg px-4 py-2 text-[12.5px]",
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** In flight: disabled, aria-busy, and busyLabel (if given) in place of the label. */
  busy?: boolean;
  busyLabel?: ReactNode;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "md", busy = false, busyLabel, disabled, className, style, children, type = "button", ...rest },
  ref,
) {
  const look = LOOK[variant];
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      className={`font-semibold disabled:cursor-default ${SIZE[size]} ${look.className} ${className ?? ""}`}
      style={{ ...look.style, ...style }}
      {...rest}
    >
      {busy && busyLabel ? busyLabel : children}
    </button>
  );
});

export interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-label"> {
  /** Required: an icon alone says nothing to a screen reader. Also the hover tooltip. */
  label: string;
  children: ReactNode;
}

// IconButton is a glyph-only button that can't ship without a name: label becomes
// both aria-label and title.
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, className, type = "button", title, children, ...rest },
  ref,
) {
  return (
    <button ref={ref} type={type} aria-label={label} title={title ?? label} className={`grid place-items-center ${className ?? ""}`} {...rest}>
      {children}
    </button>
  );
});
