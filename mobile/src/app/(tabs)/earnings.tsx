import { EarningsView } from "../profile/earnings";

// Earnings as a tab (supporter-only app, 2026-11-06): for a supporter it is
// the screen that matters most, so it is one tap away instead of under
// Profile. Same view as /profile/earnings, without the Back control.
export default function EarningsTab() {
  return <EarningsView asTab />;
}
