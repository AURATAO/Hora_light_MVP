import { useFocusEffect } from "expo-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { AppState, Image, Text, View } from "react-native";
import MapView, { Marker, PROVIDER_DEFAULT, type Region } from "react-native-maps";
import { getLiveLocation } from "../lib/api";
import {
  formatDistance,
  formatUpdatedAgo,
  liveStateDetail,
  liveStateLabel,
} from "../lib/live-tracking";
import type { LiveLocation } from "../lib/types";
import { color } from "../theme/tokens";

/**
 * The requester's live view of their supporter: one status card, one map, two
 * markers. The #1 ask out of Traction 3, and the mobile half of
 * app/src/components/LiveTrackingCard.jsx — same states, same copy, same
 * marker design.
 *
 * What this deliberately does NOT do: no route polyline, no ETA, no animated
 * marker gliding between fixes. A polyline is a road we did not measure, an
 * ETA is a promise we cannot keep, and interpolation invents positions the
 * supporter's phone never reported. The markers jump on each poll, which is
 * exactly as often as the truth changes.
 *
 * DESIGN.md §1 says no shadows outside the tab bar and sheets. The two map
 * markers take the one exception in this file, and only them: a pin with no
 * shadow disappears into a satellite roof or a pale street, and these are not
 * app chrome — they sit on somebody else's canvas, where the rule's reasoning
 * (depth from page/surface contrast) does not reach.
 */

const POLL_INTERVAL_MS = 15_000;
/** Ages the "updated 12s ago" line between polls so it counts up. */
const CLOCK_TICK_MS = 1_000;

const SUPPORTER_PIN = 48;
/** Ring thickness. Not on the 4pt grid because it is a stroke, not spacing. */
const PIN_RING = 3;

// The one shadow in this file. Shared by both markers so they read as one set.
const PIN_SHADOW = {
  shadowColor: color.ink,
  shadowOpacity: 0.3,
  shadowRadius: 4,
  shadowOffset: { width: 0, height: 2 },
  elevation: 4,
} as const;

/**
 * The HO:RA mark at pin scale: brand disc, gold colon. Used when the supporter
 * has no avatar, and when the one they have fails to load — a torn-image icon
 * sitting on somebody's street is worse than no photo at all.
 *
 * The colon is DRAWN, as two dots, not typed. A glyph would need a font size
 * scaled off the pin — raw type values, which DESIGN.md §8 only tolerates
 * inside ui/ — and a colon set in the system face reads as punctuation at this
 * size rather than as the logo. Two dots is what the wordmark actually is.
 * The one place gold appears on this screen (§1: max once).
 */
const LOGO_DOT = 5;

function LogoDot() {
  return (
    <View
      className="h-full w-full items-center justify-center gap-1 rounded-pill bg-brand"
      accessibilityElementsHidden
    >
      <View
        className="rounded-pill bg-gold"
        style={{ width: LOGO_DOT, height: LOGO_DOT }}
      />
      <View
        className="rounded-pill bg-gold"
        style={{ width: LOGO_DOT, height: LOGO_DOT }}
      />
    </View>
  );
}

/**
 * The supporter's marker: their face, ringed in brand green, with a pointer
 * tail. Grey and faded once the server calls the position stale — the
 * requester still wants to see where they WERE, and the card above says
 * plainly that it is not current.
 */
function SupporterPin({ avatarUrl, stale }: { avatarUrl: string; stale: boolean }) {
  const [broken, setBroken] = useState(false);
  const ring = stale ? color.muted : color.brand;

  return (
    <View className="items-center">
      <View
        style={{
          width: SUPPORTER_PIN,
          height: SUPPORTER_PIN,
          borderRadius: SUPPORTER_PIN / 2,
          borderWidth: PIN_RING,
          borderColor: ring,
          backgroundColor: color.surface,
          overflow: "hidden",
          opacity: stale ? 0.75 : 1,
          ...PIN_SHADOW,
        }}
      >
        {avatarUrl && !broken ? (
          <Image
            source={{ uri: avatarUrl }}
            onError={() => setBroken(true)}
            style={{ width: "100%", height: "100%" }}
            resizeMode="cover"
          />
        ) : (
          <LogoDot />
        )}
      </View>
      {/* The tail, as a CSS triangle: a zero-size box with a coloured top
          border. Keeps the pin pointing AT a spot rather than sitting near
          one, and needs no asset on either platform. */}
      <View
        style={{
          width: 0,
          height: 0,
          marginTop: -2,
          borderLeftWidth: 7,
          borderRightWidth: 7,
          borderTopWidth: 10,
          borderLeftColor: color.transparent,
          borderRightColor: color.transparent,
          borderTopColor: ring,
          backgroundColor: color.transparent,
        }}
      />
    </View>
  );
}

/**
 * Location A. A home glyph on an ink teardrop — deliberately a different SHAPE
 * from the supporter's circle, not just a different colour, so the two can
 * never be confused at a glance or by someone who does not see colour.
 */
function DestinationPin() {
  return (
    <View
      className="items-center justify-center rounded-pill border-2 border-white bg-ink"
      style={{ width: 30, height: 30, ...PIN_SHADOW }}
    >
      <View
        style={{
          width: 0,
          height: 0,
          borderLeftWidth: 7,
          borderRightWidth: 7,
          borderBottomWidth: 7,
          borderLeftColor: color.transparent,
          borderRightColor: color.transparent,
          borderBottomColor: color.surface,
        }}
      />
      <View style={{ width: 10, height: 6, backgroundColor: color.surface, marginTop: -1 }} />
    </View>
  );
}

/** Enough padding around the two points that neither pin sits on the edge. */
function regionFor(
  supporter: { lat: number; lng: number } | null,
  destination: { lat: number; lng: number } | null
): Region | null {
  const points = [supporter, destination].filter(Boolean) as { lat: number; lng: number }[];
  if (points.length === 0) return null;
  const lats = points.map((p) => p.lat);
  const lngs = points.map((p) => p.lng);
  const minLat = Math.min(...lats);
  const maxLat = Math.max(...lats);
  const minLng = Math.min(...lngs);
  const maxLng = Math.max(...lngs);
  return {
    latitude: (minLat + maxLat) / 2,
    longitude: (minLng + maxLng) / 2,
    // A floor of ~0.005° (≈550m) so a supporter standing on the doorstep does
    // not zoom the map to a single building.
    latitudeDelta: Math.max((maxLat - minLat) * 1.6, 0.005),
    longitudeDelta: Math.max((maxLng - minLng) * 1.6, 0.005),
  };
}

export interface LiveTrackingCardProps {
  taskId: string;
}

/**
 * Polls GET /tasks/:id/live while this card is mounted and the app is in the
 * foreground. Backgrounding stops it: a phone in a pocket polling somebody's
 * live position every fifteen seconds is battery the requester did not agree
 * to spend on a screen they are not looking at.
 *
 * The caller decides whether to mount this at all (shouldPollLive) — the
 * server 404s outside that set, so there is no not-allowed state to render.
 */
export function LiveTrackingCard({ taskId }: LiveTrackingCardProps) {
  const [live, setLive] = useState<LiveLocation | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const mapRef = useRef<MapView | null>(null);
  const framedRef = useRef(false);

  const poll = useCallback(async () => {
    try {
      setLive(await getLiveLocation(taskId));
    } catch {
      // A dropped poll leaves the last answer on screen. It carries its own
      // "updated 40s ago", which keeps ageing whether or not the next poll
      // lands — so a blip degrades into a visibly stale card rather than an
      // error the requester has to dismiss.
    }
  }, [taskId]);

  // TWO conditions, not one. useFocusEffect covers navigation — pushing the
  // chat screen leaves this one mounted in the stack, so unmount alone would
  // never stop the timer — and the AppState listener covers backgrounding. A
  // phone in a pocket polling somebody's live position every fifteen seconds is
  // battery the requester did not agree to spend on a screen nobody is looking
  // at, and it happens through whichever of those two doors comes first.
  useFocusEffect(
    useCallback(() => {
      let timer: ReturnType<typeof setInterval> | null = null;

      const start = () => {
        if (timer) return;
        void poll();
        timer = setInterval(() => void poll(), POLL_INTERVAL_MS);
      };
      const stop = () => {
        if (!timer) return;
        clearInterval(timer);
        timer = null;
      };

      if (AppState.currentState === "active") start();
      const sub = AppState.addEventListener("change", (next) => {
        if (next === "active") start();
        else stop();
      });

      return () => {
        stop();
        sub.remove();
      };
    }, [poll])
  );

  useEffect(() => {
    const tick = setInterval(() => setNow(Date.now()), CLOCK_TICK_MS);
    return () => clearInterval(tick);
  }, []);

  const supporterPos =
    live?.lat != null && live?.lng != null ? { lat: live.lat, lng: live.lng } : null;
  const destination = live?.destination ?? null;

  // Frame both markers ONCE. Re-framing on every poll would fight the
  // requester's own pan and zoom every fifteen seconds, which is the fastest
  // way to make a map feel broken.
  useEffect(() => {
    if (framedRef.current) return;
    const region = regionFor(supporterPos, destination);
    if (!region || !mapRef.current) return;
    framedRef.current = true;
    mapRef.current.animateToRegion(region, 400);
  }, [supporterPos?.lat, supporterPos?.lng, destination?.lat, destination?.lng]);

  if (!live) return null;

  const stale = live.state === "unavailable";
  const supporterName = live.supporter?.name ?? "";
  const distance = formatDistance(live.distance_m);
  const updated = formatUpdatedAgo(live.updated_at, now);
  const initialRegion = regionFor(supporterPos, destination);

  return (
    <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
      <Text className="text-caption font-semibold text-muted">Live</Text>

      <View className="flex-row items-start justify-between gap-3">
        <View className="flex-1">
          <View className="flex-row items-center gap-2">
            <View
              className={`h-2 w-2 rounded-pill ${stale ? "bg-inactive" : "bg-brand"}`}
              accessibilityElementsHidden
            />
            <Text className="text-title font-semibold text-ink">{liveStateLabel(live.state)}</Text>
          </View>
          <Text className="mt-1 text-caption text-muted">
            {liveStateDetail(live.state, supporterName)}
          </Text>
        </View>
        {/* Absent rather than zeroed when the server withheld it — the same
            rule the payment copy follows. Never a coordinate. */}
        {distance ? (
          <Text className="text-body font-semibold text-ink">{distance}</Text>
        ) : null}
      </View>

      {updated ? <Text className="text-caption text-muted">{updated}</Text> : null}

      {initialRegion ? (
        <View
          className="overflow-hidden rounded-sm border border-line"
          style={{ height: 180 }}
          accessible
          accessibilityRole="image"
          accessibilityLabel={`Map showing ${liveStateDetail(live.state, supporterName)}${
            distance ? ` ${distance}` : ""
          }`}
        >
          <MapView
            ref={mapRef}
            provider={PROVIDER_DEFAULT}
            style={{ flex: 1 }}
            initialRegion={initialRegion}
            pitchEnabled={false}
            rotateEnabled={false}
            toolbarEnabled={false}
            showsUserLocation={false}
            showsPointsOfInterests={false}
          >
            {destination ? (
              <Marker
                coordinate={{ latitude: destination.lat, longitude: destination.lng }}
                anchor={{ x: 0.5, y: 0.5 }}
                title="Task address"
              >
                <DestinationPin />
              </Marker>
            ) : null}
            {supporterPos ? (
              <Marker
                // Re-keyed on the position so iOS actually redraws the custom
                // child view: react-native-maps caches the rendered marker
                // bitmap, and a coordinate change alone can leave the old one
                // on screen.
                key={`${supporterPos.lat},${supporterPos.lng},${stale}`}
                coordinate={{ latitude: supporterPos.lat, longitude: supporterPos.lng }}
                anchor={{ x: 0.5, y: 1 }}
                title={supporterName || "Supporter"}
              >
                <SupporterPin avatarUrl={live.supporter?.avatar_url ?? ""} stale={stale} />
              </Marker>
            ) : null}
          </MapView>
        </View>
      ) : null}
    </View>
  );
}
