// Handing an address or a multi-stop route off to the device's maps app.
//
// Everything here is URL-scheme only — no map SDK, no API key. Nothing in this
// file can be exercised in the simulator beyond "the URL was opened": whether
// Apple Maps / Google Maps actually renders the route is real-device territory.

import { Linking, Platform } from "react-native";

/**
 * Open a single address in the platform's default maps app.
 * iOS → Apple Maps (`maps.apple.com`, which the OS claims and opens natively).
 * Android → the `geo:` intent, which any installed maps app can handle.
 */
export async function openAddressInMaps(address: string): Promise<void> {
  const query = encodeURIComponent(address.trim());
  if (!query) return;
  const url =
    Platform.OS === "ios"
      ? `http://maps.apple.com/?q=${query}`
      : `geo:0,0?q=${query}`;
  try {
    await Linking.openURL(url);
  } catch {
    // No maps app / user cancelled the handoff — nothing useful to say.
  }
}

/**
 * Open a full route through every stop, starting from the device's current
 * position. Uses the Google Maps universal directions URL on both platforms:
 * it opens the Google Maps app when installed and falls back to the web map
 * otherwise, and unlike Apple Maps' scheme it supports waypoints.
 *
 * `origin` is deliberately omitted — Google Maps then defaults to the user's
 * current location, which is exactly what "get directions from where I am"
 * means and avoids asking for a location permission just to build a URL.
 */
export async function openRouteInMaps(stops: string[]): Promise<void> {
  const clean = stops.map((s) => s.trim()).filter(Boolean);
  if (clean.length === 0) return;

  const destination = clean[clean.length - 1];
  const waypoints = clean.slice(0, -1);

  const params = [
    "api=1",
    `destination=${encodeURIComponent(destination)}`,
    "travelmode=driving",
  ];
  if (waypoints.length > 0) {
    params.push(`waypoints=${waypoints.map(encodeURIComponent).join("%7C")}`);
  }

  try {
    await Linking.openURL(`https://www.google.com/maps/dir/?${params.join("&")}`);
  } catch {
    // Fall back to plain point-to-point navigation in the platform maps app.
    await openAddressInMaps(destination);
  }
}
