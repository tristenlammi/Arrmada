// The "Get notified when it's ready?" prompt after a request. Discover and the Books tab
// announce a successful request; the layout's PushPromptHost hears it and, unless this
// device has already answered, loads the prompt. Kept tiny: it's in the first load.

export const REQUESTED_EVENT = "arrmada:requested";
const KEY = "arrmada.pushPrompt";

// announceRequested says a request just went through (a new one, or joining one).
export function announceRequested() {
  window.dispatchEvent(new CustomEvent(REQUESTED_EVENT));
}

// Whether the prompt was shown this page load: the fallback when storage is blocked, so a
// private window still sees it at most once per load rather than after every request.
let shownThisLoad = false;

// promptAnswered: this device said "Not now" / "Got it" ("dismissed"), or turned push on
// from it ("done") — either way it never asks again here.
export function promptAnswered(): boolean {
  if (shownThisLoad) return true;
  try {
    const v = localStorage.getItem(KEY);
    return v === "dismissed" || v === "done";
  } catch {
    return false;
  }
}

export function markPromptShown() {
  shownThisLoad = true;
}

export function rememberPrompt(answer: "dismissed" | "done") {
  try { localStorage.setItem(KEY, answer); } catch { /* blocked storage: shownThisLoad covers this load */ }
}
