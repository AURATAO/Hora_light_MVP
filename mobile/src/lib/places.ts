// Google Places API (New) over plain REST — no native SDK, no JS Maps loader.
//
// Mirrors web's PlaceInput (app/src/components/PlaceInput.jsx), which uses the
// google.maps.places.AutocompleteSuggestion / Place JS classes. The REST
// endpoints below are the same service: a session token ties the autocomplete
// calls to the one Details call that closes the session (Google bills the pair
// once), and the NYC location bias is byte-for-byte the same circle web sends.

import { Platform } from "react-native";
import Constants from "expo-constants";

const API_KEY = process.env.EXPO_PUBLIC_GOOGLE_PLACES_KEY;

const AUTOCOMPLETE_URL = "https://places.googleapis.com/v1/places:autocomplete";
const DETAILS_URL = "https://places.googleapis.com/v1/places";

// Same circle as web's AddressInput NYC_BIAS: { lat 40.7128, lng -74.006, radius 40000 }.
const NYC_BIAS = {
  circle: {
    center: { latitude: 40.7128, longitude: -74.006 },
    radius: 40000,
  },
} as const;

export interface PlaceSuggestion {
  placeId: string;
  /** Full prediction text — what the row shows and what we fall back to as the label. */
  text: string;
  /** Street line, when Google splits it out. */
  mainText: string;
  /** City / state / country line, when Google splits it out. */
  secondaryText: string;
}

export interface PlaceDetails {
  label: string;
  placeId: string | null;
  lat: number | null;
  lng: number | null;
}

/**
 * App-identity headers. Our key is restricted to this app in Google Cloud, and a
 * plain `fetch` — unlike the native Maps SDKs — carries no app identity, so the
 * REST call is rejected with API_KEY_IOS_APP_BLOCKED unless we send it ourselves
 * (verified live against places:autocomplete: 403 without, 200 with).
 *
 * Android note: Google's Android key restriction checks `X-Android-Package`
 * AND `X-Android-Cert` (the signing SHA-1), and the fingerprint isn't reachable
 * from JS. If the key is ever restricted to Android apps, this call will 403 on
 * Android; restrict that key by API instead, or mint a separate unrestricted-
 * by-app key for the Android build.
 */
function appIdentityHeaders(): Record<string, string> {
  const config = Constants.expoConfig;
  if (Platform.OS === "ios") {
    const bundleId = config?.ios?.bundleIdentifier;
    return bundleId ? { "X-Ios-Bundle-Identifier": bundleId } : {};
  }
  if (Platform.OS === "android") {
    const pkg = config?.android?.package;
    return pkg ? { "X-Android-Package": pkg } : {};
  }
  return {};
}

export function placesConfigured(): boolean {
  return !!API_KEY;
}

/**
 * A session token groups the keystroke-by-keystroke autocomplete calls with the
 * single Details call that follows. Any opaque string works; Google only needs
 * it to be unique per session (see Places API session-token docs).
 */
export function newSessionToken(): string {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

interface AutocompleteResponse {
  suggestions?: {
    placePrediction?: {
      placeId?: string;
      text?: { text?: string };
      structuredFormat?: {
        mainText?: { text?: string };
        secondaryText?: { text?: string };
      };
    };
  }[];
}

export async function fetchSuggestions(
  input: string,
  sessionToken: string,
  signal?: AbortSignal
): Promise<PlaceSuggestion[]> {
  if (!API_KEY) return [];
  const res = await fetch(AUTOCOMPLETE_URL, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Goog-Api-Key": API_KEY,
      ...appIdentityHeaders(),
    },
    body: JSON.stringify({ input, locationBias: NYC_BIAS, sessionToken }),
    signal,
  });
  if (!res.ok) throw new Error(`Places autocomplete failed (${res.status})`);
  const json = (await res.json()) as AutocompleteResponse;
  return (json.suggestions ?? [])
    .map((s) => s.placePrediction)
    .filter((p): p is NonNullable<typeof p> => !!p?.placeId && !!p.text?.text)
    .map((p) => ({
      placeId: p.placeId as string,
      text: p.text?.text as string,
      mainText: p.structuredFormat?.mainText?.text ?? (p.text?.text as string),
      secondaryText: p.structuredFormat?.secondaryText?.text ?? "",
    }));
}

interface DetailsResponse {
  id?: string;
  formattedAddress?: string;
  displayName?: { text?: string };
  location?: { latitude?: number; longitude?: number };
}

/**
 * Resolves a suggestion to its formatted address, same field set web asks for
 * (id, displayName, formattedAddress, location) and the same label precedence:
 * formattedAddress → displayName → the prediction text.
 */
export async function fetchPlaceDetails(
  placeId: string,
  fallbackText: string,
  sessionToken: string
): Promise<PlaceDetails> {
  if (!API_KEY) return { label: fallbackText, placeId: null, lat: null, lng: null };
  const url =
    `${DETAILS_URL}/${encodeURIComponent(placeId)}` +
    `?sessionToken=${encodeURIComponent(sessionToken)}`;
  const res = await fetch(url, {
    headers: {
      "X-Goog-Api-Key": API_KEY,
      "X-Goog-FieldMask": "id,displayName,formattedAddress,location",
      ...appIdentityHeaders(),
    },
  });
  if (!res.ok) throw new Error(`Places details failed (${res.status})`);
  const json = (await res.json()) as DetailsResponse;
  return {
    label: json.formattedAddress || json.displayName?.text || fallbackText,
    placeId: json.id ?? placeId,
    lat: typeof json.location?.latitude === "number" ? json.location.latitude : null,
    lng: typeof json.location?.longitude === "number" ? json.location.longitude : null,
  };
}

/**
 * Insert an apt/suite/floor after the street segment, exactly as web's
 * AddressInput does (app/src/components/AddressInput.jsx emit): after the first
 * comma, or appended when the label has no comma. Keeping this identical is what
 * makes location_text format-compatible across the two clients.
 */
export function withApt(label: string, apt: string): string {
  const trimmed = apt.trim();
  if (!trimmed) return label;
  const commaIdx = label.indexOf(",");
  return commaIdx !== -1
    ? `${label.slice(0, commaIdx)}, ${trimmed}${label.slice(commaIdx)}`
    : `${label}, ${trimmed}`;
}
