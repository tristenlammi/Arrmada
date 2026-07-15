export function FleetMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" className={className} aria-hidden>
      <path d="M12 2 L20 7 L12 12 L4 7 Z" fill="currentColor" />
      <path d="M12 9 L20 14 L12 19 L4 14 Z" fill="currentColor" opacity="0.6" />
      <path d="M12 16 L20 21 L12 24 L4 21 Z" fill="currentColor" opacity="0.32" />
    </svg>
  );
}
