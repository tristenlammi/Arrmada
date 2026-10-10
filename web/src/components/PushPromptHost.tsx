import { Suspense, useCallback, useEffect, useState } from "react";
import { lazyPage } from "../lib/lazyPage";
import { promptAnswered, REQUESTED_EVENT } from "../lib/pushPrompt";

// The prompt and the push code behind it download only when someone has just requested
// something and this device hasn't answered it yet.
const PushPrompt = lazyPage(() => import("./PushPrompt"), "PushPrompt");

// PushPromptHost waits, in every layout, for a successful request ("arrmada:requested") and
// then offers push once. Never on page load: only right after a real request.
export function PushPromptHost() {
  const [asked, setAsked] = useState(false);
  // Stable, so the prompt's status check runs once per showing.
  const done = useCallback(() => setAsked(false), []);
  useEffect(() => {
    const onRequested = () => { if (!promptAnswered()) setAsked(true); };
    window.addEventListener(REQUESTED_EVENT, onRequested);
    return () => window.removeEventListener(REQUESTED_EVENT, onRequested);
  }, []);
  if (!asked) return null;
  return (
    <Suspense fallback={null}>
      <PushPrompt onDone={done} />
    </Suspense>
  );
}
