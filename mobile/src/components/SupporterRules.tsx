import { useEffect, useState } from "react";
import { Text, View } from "react-native";
import { Ban, DoorOpen, Footprints, Hand, ShieldAlert } from "lucide-react-native";
import type { LucideIcon } from "lucide-react-native";
import { Card } from "./ui";
import { getAppConfig } from "../lib/api";
import { BUILT_IN_RULES, rulesFromConfig, type SupporterRule } from "../lib/supporter-rules";
import { color, size } from "../theme/tokens";

// The supporter rules: what a supporter may always do, and what they never
// do. Shown in Profile → Supporter rules and on the application form.
//
// The list is the SERVER's when GET /config carries one (supporter-rules.ts),
// so wording can change after a legal review without an app release; the
// built-in list shows at once and stays when the server sends none.

const ICONS: Record<string, LucideIcon> = {
  hand: Hand,
  door: DoorOpen,
  shield: ShieldAlert,
  footprints: Footprints,
  ban: Ban,
};

export function SupporterRules() {
  const [rules, setRules] = useState<SupporterRule[]>(BUILT_IN_RULES);

  useEffect(() => {
    let live = true;
    getAppConfig()
      .then((cfg) => {
        if (live) setRules(rulesFromConfig(cfg.supporter_rules));
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);

  return (
    <Card>
      <View className="gap-4">
        {rules.map((r) => {
          const Icon = ICONS[r.icon] ?? ShieldAlert;
          return (
            <View key={r.title} className="flex-row items-start gap-3">
              <Icon color={color.ink} size={18} strokeWidth={size.iconStroke} />
              <View className="flex-1">
                <Text className="text-body font-semibold text-ink">{r.title}</Text>
                <Text className="mt-1 text-caption text-muted">{r.body}</Text>
              </View>
            </View>
          );
        })}
      </View>
    </Card>
  );
}
