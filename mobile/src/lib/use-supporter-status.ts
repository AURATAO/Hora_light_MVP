import { useCallback, useState } from "react";
import { useFocusEffect } from "expo-router";
import { getProfile } from "./api";
import type { SupporterStatus } from "./types";

/**
 * "Is this person an approved supporter?" — the one question every
 * supporter-only surface is gated on.
 *
 * TRI-STATE, AND THE UNKNOWN STATE MATTERS. `null` means not yet known, and
 * callers must not collapse it into "no": the tab bar is built from this, and
 * flashing a three-tab bar into a four-tab one on every cold start is worse
 * than waiting a beat. Screens that merely hide a row can treat null as not-
 * approved, because appearing late is harmless there.
 *
 * A FAILED READ STAYS `null` for the same reason: an expired session or no
 * network is not evidence that somebody stopped being a supporter, and
 * answering "no" would take the Earn tab away from a supporter standing in a
 * dead spot.
 *
 * Re-read on FOCUS rather than once on mount. Approval happens out of band —
 * ops approve an application while the app is backgrounded — and a supporter
 * who has just been approved should find the tab there when they come back,
 * not after a reinstall.
 */
export function useSupporterStatus(): {
  status: SupporterStatus | null;
  isApproved: boolean;
} {
  const [status, setStatus] = useState<SupporterStatus | null>(null);

  useFocusEffect(
    useCallback(() => {
      let alive = true;
      getProfile()
        .then((p) => {
          if (alive) setStatus(p.supporter_status);
        })
        .catch(() => {
          // Unknown, and it stays unknown. See the note above.
        });
      return () => {
        alive = false;
      };
    }, [])
  );

  return { status, isApproved: status === "approved" };
}
