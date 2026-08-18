import { useState } from "react";

export interface BetaNoticeGate {
  /** Bind to BetaNoticeSheet's `visible`. */
  open: boolean;
  /** Accepted — closes the sheet and silences it for the rest of this app run. */
  acknowledge: () => void;
  /** Backed out without accepting — the sheet returns on the next entry. */
  dismiss: () => void;
}

// Module scope, so it lives exactly as long as the JS bundle does: the notice
// shows on the first Post Task entry per app-open and stays quiet for the rest
// of that run. Deliberately not read from `profile.beta_accepted` — that flag
// records the previous round's terms, and this round's window and pricing are
// new, so everyone sees them once regardless of what they accepted before.
let acknowledgedThisSession = false;

/**
 * The beta notice gate on Post Task. Mirrors useCompanionshipGate's shape: the
 * host screen owns what accepting persists, this owns only when to ask.
 */
export function useBetaNoticeGate(): BetaNoticeGate {
  const [open, setOpen] = useState(!acknowledgedThisSession);

  return {
    open,
    acknowledge: () => {
      acknowledgedThisSession = true;
      setOpen(false);
    },
    // No session flag: a user who backs out of Post Task hasn't agreed to
    // anything, so the notice is still owed the next time they come in.
    dismiss: () => setOpen(false),
  };
}
