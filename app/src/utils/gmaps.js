// 最穩定的連結優先序：placeId > lat,lng > label 文字查詢
export function gmapsPlaceUrl({ placeId, id, lat, lng, label }) {
  const pid = placeId || id; // 新 Places 可能只有 id
  if (pid) {
    return `https://www.google.com/maps/place/?q=place_id:${encodeURIComponent(
      pid
    )}`;
  }
  if (typeof lat === "number" && typeof lng === "number") {
    return `https://www.google.com/maps/search/?api=1&query=${lat},${lng}`;
  }
  const q = (label || "").trim();
  return q
    ? `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(q)}`
    : "#";
}

// （可選）多點路線：origin -> waypoints -> destination
export function gmapsDirectionsUrl(points = []) {
  const cleaned = points.filter(
    (p) =>
      p && (p.label || p.placeId || p.id || (p.lat != null && p.lng != null))
  );
  if (cleaned.length < 2) return "#";
  const enc = (p) =>
    p.placeId || p.id
      ? `place_id:${p.placeId || p.id}`
      : typeof p.lat === "number" && typeof p.lng === "number"
      ? `${p.lat},${p.lng}`
      : p.label || "";
  const origin = enc(cleaned[0]);
  const destination = enc(cleaned[cleaned.length - 1]);
  const waypoints = cleaned.slice(1, -1).map(enc).join("|");
  const params = new URLSearchParams({ api: "1", origin, destination });
  if (waypoints) params.set("waypoints", waypoints);
  return `https://www.google.com/maps/dir/?${params.toString()}`;
}
