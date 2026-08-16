import { useEffect, useState } from "react";
import { isCompanionCategory } from "./companionship-policy";
import type { TaskCategory } from "./types";

export interface CompanionshipGate {
  /** Submission must be blocked while true — the policy hasn't been accepted. */
  needsPolicy: boolean;
  /** Bind to CompanionshipPolicySheet's `visible`. */
  open: boolean;
  /** Call from a blocked submit to put the sheet back in front of the user. */
  request: () => void;
  acknowledge: () => void;
  dismiss: () => void;
}

export interface CompanionshipGateOptions {
  /**
   * Whether the form this gate guards is on screen and settled. The sheet opens
   * the moment companionship becomes real — landing on a form that already has
   * it selected, or switching the category to it — so a form still loading its
   * initial values must pass false until it has them.
   */
  active: boolean;
  /**
   * Treat the policy as already accepted. Editing a task that was posted as
   * companionship falls here: it was acknowledged when it was posted, and
   * re-asking on every edit would be noise. Read on each render rather than
   * seeded into state, so it can arrive late with a fetched task.
   */
  preAcknowledged?: boolean;
}

/**
 * The companionship policy gate shared by Post Task and task editing: a task in
 * that category can't be submitted until the policy sheet has been accepted in
 * this flow. Acknowledgement lasts for the lifetime of the screen holding the
 * hook, so switching the category away and back never re-asks — only a fresh
 * flow does.
 */
export function useCompanionshipGate(
  category: TaskCategory | undefined,
  { active, preAcknowledged = false }: CompanionshipGateOptions
): CompanionshipGate {
  const [acknowledged, setAcknowledged] = useState(false);
  const [open, setOpen] = useState(false);

  const needsPolicy = isCompanionCategory(category) && !acknowledged && !preAcknowledged;

  // Deps are active+category only — dismissing without acknowledging must not
  // immediately re-open the sheet. Submission stays blocked by `needsPolicy`
  // instead, and the host screen re-opens it via `request()`.
  useEffect(() => {
    if (active && needsPolicy) setOpen(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, category]);

  return {
    needsPolicy,
    open,
    request: () => setOpen(true),
    acknowledge: () => {
      setAcknowledged(true);
      setOpen(false);
    },
    dismiss: () => setOpen(false),
  };
}
