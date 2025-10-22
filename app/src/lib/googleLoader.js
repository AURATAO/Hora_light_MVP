import { setOptions, importLibrary } from "@googlemaps/js-api-loader";

let booted = false;

export function ensureMapsBoot() {
  if (booted) return;
  const key = import.meta.env.VITE_GOOGLE_MAPS_API_KEY;
  if (!key) throw new Error("Missing VITE_GOOGLE_MAPS_API_KEY");
  setOptions({ key, v: "weekly" }); // 可加 region/language 等
  booted = true;
}

// 需要 Maps（不是必需）
export async function loadMapsLib() {
  ensureMapsBoot();
  await importLibrary("maps");
}

// ✅ 我們要的 Places（Autocomplete + Place）
export async function loadPlacesLib() {
  ensureMapsBoot();
  await importLibrary("places");
}
