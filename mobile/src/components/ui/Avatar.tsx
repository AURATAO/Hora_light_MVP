import { Image, Text, View } from "react-native";

export interface AvatarProps {
  uri?: string | null;
  name?: string | null;
  size?: number;
}

function initialsFor(name?: string | null): string {
  const parts = (name ?? "").trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  const first = parts[0]?.[0] ?? "";
  const last = parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? "") : "";
  return (first + last).toUpperCase();
}

// avatar_url image, falling back to initials on a brandTint circle (always
// paired with brand text per DESIGN.md §1). No icon fallback — profiles
// always have a name by the time they're shown here.
export function Avatar({ uri, name, size = 44 }: AvatarProps) {
  if (uri) {
    return <Image source={{ uri }} style={{ width: size, height: size, borderRadius: size / 2 }} />;
  }

  return (
    <View
      className="items-center justify-center rounded-pill bg-brand-tint"
      style={{ width: size, height: size }}
    >
      <Text className="font-semibold text-brand" style={{ fontSize: size * 0.36 }}>
        {initialsFor(name)}
      </Text>
    </View>
  );
}
