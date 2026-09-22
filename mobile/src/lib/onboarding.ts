import type { Profile } from "./types";

// Single source of truth for the post-login onboarding gate, shared by the
// root gate (app/index.tsx) and both onboarding screens so they always agree
// on what "done" means.
//
// Web (app/src/) runs the same two gates: a beta-acceptance modal keyed on
// `beta_accepted`, and a profile gate on `phone` + `avatar_url`. The
// complete-profile SCREEN requires a name as well on both clients — that is
// "display name required at signup" — but the gate itself does not key on
// it, because an existing account can be nameless: until build 12 every
// profile was seeded with the email's local part, and that seed has been
// blanked (migration 20260922075623). Sending those accounts back through
// onboarding for one field would be wrong; they get NamePromptSheet instead,
// once, skippable (needsNamePrompt).

export function needsBetaAccept(profile: Profile | null): boolean {
  return !profile?.beta_accepted;
}

export function needsProfileCompletion(profile: Profile | null): boolean {
  if (!profile) return true;
  return !profile.phone?.trim() || !profile.avatar_url?.trim();
}

/** A finished profile with no display name: ask "What should we call you?". */
export function needsNamePrompt(profile: Profile | null): boolean {
  if (!profile || needsProfileCompletion(profile)) return false;
  return !profile.name?.trim();
}
