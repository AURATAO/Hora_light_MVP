import type { Profile } from "./types";

// Single source of truth for the post-login onboarding gate, shared by the
// root gate (app/index.tsx) and both onboarding screens so they always agree
// on what "done" means.
//
// Web (app/src/) runs two gates: a beta-acceptance modal keyed on
// `beta_accepted`, and a profile gate that requires `phone` + `avatar_url`
// (name is optional there). Mobile mirrors the beta gate and additionally
// requires a `name` on the profile step — see feature/mobile-onboarding.

export function needsBetaAccept(profile: Profile | null): boolean {
  return !profile?.beta_accepted;
}

export function needsProfileCompletion(profile: Profile | null): boolean {
  if (!profile) return true;
  return !profile.name?.trim() || !profile.phone?.trim() || !profile.avatar_url?.trim();
}
