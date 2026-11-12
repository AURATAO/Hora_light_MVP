import { Text, View } from "react-native";
import { DoorOpen, Footprints, Hand, ShieldAlert } from "lucide-react-native";
import type { LucideIcon } from "lucide-react-native";
import { Card } from "./ui";
import { color, size } from "../theme/tokens";

// The supporter rules, in one place: the Supporter rules screen
// (profile/rules.tsx) and the supporter application both render this list,
// so the two can never say different things.
export const SUPPORTER_RULES: { icon: LucideIcon; title: string; body: string }[] = [
  {
    icon: Hand,
    title: "You can always say no",
    body: "Decline any task, or leave one, if you feel unsafe. There's no penalty, and you don't have to explain.",
  },
  {
    icon: DoorOpen,
    title: "Homes only for a specific errand",
    body: "Go inside a home only for the errand you accepted, with the customer or an adult they named present, or with clear access instructions from them.",
  },
  {
    icon: ShieldAlert,
    title: "Never alone with minors",
    body: "If you would be left alone with someone under 18, stop and leave the task.",
  },
  {
    // Pilot (Aura, 2026-11-12): never transport people. A Supporter may use
    // their own vehicle to carry ITEMS when the task says so. Companionship is
    // off for the pilot; its rule comes back with the feature.
    icon: Footprints,
    title: "Never drive a customer",
    body: "Never give anyone a ride. You can use your own car to carry items when the task says so.",
  },
];

export function SupporterRules() {
  return (
    <Card>
      <View className="gap-4">
        {SUPPORTER_RULES.map((r) => (
          <View key={r.title} className="flex-row items-start gap-3">
            <r.icon color={color.ink} size={18} strokeWidth={size.iconStroke} />
            <View className="flex-1">
              <Text className="text-body font-semibold text-ink">{r.title}</Text>
              <Text className="mt-1 text-caption text-muted">{r.body}</Text>
            </View>
          </View>
        ))}
      </View>
    </Card>
  );
}
