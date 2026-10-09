// ConfirmDialog moved into the UI kit (src/ui) so it renders through the shared
// Modal. This re-export keeps existing imports working while pages migrate to
// `import { ConfirmDialog } from "../ui"` (or useConfirm for a plain yes/no).
export { ConfirmDialog, type ConfirmChoice } from "../ui/ConfirmDialog";
