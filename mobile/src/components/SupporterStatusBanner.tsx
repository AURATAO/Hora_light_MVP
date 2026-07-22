import { Alert, Linking, Text, View } from "react-native";
import { CircleX, Clock } from "lucide-react-native";
import { Badge, Button, PressableScale } from "./ui";
import { SUPPORT_EMAIL } from "../lib/constants";
import type { SupporterStatus } from "../lib/types";
import { color, size } from "../theme/tokens";

export interface SupporterStatusBannerProps {
  status: SupporterStatus;
  onApply: () => void;
}

async function contactSupport() {
  const url = `mailto:${SUPPORT_EMAIL}?subject=${encodeURIComponent("Supporter application")}`;
  try {
    await Linking.openURL(url);
  } catch {
    Alert.alert("No mail app", `Write to us at ${SUPPORT_EMAIL}.`);
  }
}

// Mirrors web's SupporterStatusBanner semantics (approved renders nothing, the
// other three states each get a card) in the mobile design system. The status
// itself is derived server-side (S-05); this only chooses what to show.
export function SupporterStatusBanner({ status, onApply }: SupporterStatusBannerProps) {
  if (status === "approved") return null;

  if (status === "applied") {
    return (
      <View className="gap-3 rounded-card border border-line bg-surface p-4">
        <Clock color={color.muted} size={24} strokeWidth={size.iconStroke} />
        <View className="gap-1">
          <Text className="text-title font-semibold text-ink">Application under review</Text>
          <Text className="text-body text-muted">
            We&apos;ve received your application and are reviewing it. This usually takes 1–3 business days.
          </Text>
        </View>
      </View>
    );
  }

  if (status === "rejected") {
    return (
      <View className="gap-3 rounded-card border border-line bg-surface p-4">
        <CircleX color={color.danger} size={24} strokeWidth={size.iconStroke} />
        <View className="gap-1">
          <Text className="text-title font-semibold text-ink">Application not approved</Text>
          <Text className="text-body text-muted">
            We couldn&apos;t approve your application this time. Get in touch if you&apos;d like to know more or
            want to apply again.
          </Text>
        </View>
        <Button label="Contact support" variant="secondary" onPress={contactSupport} />
      </View>
    );
  }

  return (
    <View className="gap-3 rounded-card border border-line bg-surface p-4">
      <View className="gap-1">
        <Text className="text-title font-semibold text-ink">Become a verified supporter</Text>
        <Text className="text-body text-muted">
          To accept tasks on HO:RA, complete a quick background check.
        </Text>
      </View>
      <Button label="Apply now" onPress={onApply} />
    </View>
  );
}

export interface SupporterStatusRowProps {
  status: SupporterStatus;
}

// Compact sibling of the banner above for the Profile tab: same three states and
// the same support action, at row scale instead of card scale. `none` renders
// nothing — Profile owns that case as a hint pointing at the Work tab, where the
// full banner (and the apply CTA) lives.
export function SupporterStatusRow({ status }: SupporterStatusRowProps) {
  if (status === "approved") {
    return (
      <View className="items-center">
        <Badge label="Verified supporter" variant="success" />
      </View>
    );
  }

  if (status === "applied") {
    // Profile's single gold element (DESIGN.md §1: max once per screen) — the
    // rest of the screen is ink/muted/brand, so the scarcity rule holds.
    return (
      <View className="flex-row items-center gap-3 rounded-card bg-gold-tint p-4">
        <Clock color={color.goldText} size={18} strokeWidth={size.iconStroke} />
        <Text className="text-body text-gold-text">Application under review</Text>
      </View>
    );
  }

  if (status === "rejected") {
    return (
      <View className="flex-row items-center gap-3 rounded-card border border-line bg-surface p-4">
        <CircleX color={color.danger} size={18} strokeWidth={size.iconStroke} />
        <View className="flex-1 gap-1">
          <Text className="text-body text-danger">Application not approved</Text>
          <PressableScale onPress={contactSupport} hitSlop={8} className="self-start">
            <Text className="text-caption font-semibold text-brand">Contact support</Text>
          </PressableScale>
        </View>
      </View>
    );
  }

  return null;
}
