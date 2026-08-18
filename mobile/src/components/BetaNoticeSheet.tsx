import { useState } from "react";
import { Modal, Pressable, ScrollView, Text, View, useWindowDimensions } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { Button, Checkbox } from "./ui";
import { BETA_NOTICE_COPY } from "../lib/beta-notice";
import { space } from "../theme/tokens";

export interface BetaNoticeSheetProps {
  visible: boolean;
  /** Backed out without accepting (backdrop tap, Android back). */
  onDismiss: () => void;
  /**
   * Checkbox ticked and CTA pressed. Persists the acknowledgement; the sheet
   * shows its loading state until this settles, and closes only on success —
   * a rejection leaves the notice up with the box still ticked.
   */
  onAccept: () => Promise<void>;
}

// Same clamps as CompanionshipPolicySheet: the sheet takes at most this much of
// the screen, and the scrollable body gets what's left after the pinned footer.
const SHEET_MAX_HEIGHT_RATIO = 0.85;
const BODY_MAX_HEIGHT_RATIO = 0.7;

// The beta terms for the current test round, shown once per app-open on Post
// Task (see useBetaNoticeGate). Ported from web's BetaModal: same hierarchy —
// heading, intro, bullets, fine print, checkbox — with the CTA pinned to a
// footer instead of scrolling with the body.
export function BetaNoticeSheet({ visible, onDismiss, onAccept }: BetaNoticeSheetProps) {
  const { height } = useWindowDimensions();
  const insets = useSafeAreaInsets();
  const [checked, setChecked] = useState(false);
  const [saving, setSaving] = useState(false);

  async function handleAccept() {
    setSaving(true);
    try {
      await onAccept();
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      visible={visible}
      transparent
      animationType="slide"
      onRequestClose={saving ? undefined : onDismiss}
    >
      <View className="flex-1 justify-end">
        {/* Backdrop is its own layer rather than a Pressable wrapping the
            sheet: a ScrollView nested inside a pressable parent loses the
            responder negotiation on drag (see CompanionshipPolicySheet). */}
        <Pressable
          className="absolute inset-0 bg-ink/40"
          onPress={saving ? undefined : onDismiss}
        />

        <View
          className="rounded-t-card bg-surface"
          style={{ maxHeight: height * SHEET_MAX_HEIGHT_RATIO }}
        >
          <View className="px-6 pb-4 pt-6">
            <Text className="text-display text-ink">{BETA_NOTICE_COPY.heading}</Text>
          </View>

          <ScrollView
            // flexShrink alongside maxHeight: without it the footer gets pushed
            // off the bottom of the clamped sheet on shorter screens.
            style={{ maxHeight: height * BODY_MAX_HEIGHT_RATIO, flexShrink: 1 }}
            showsVerticalScrollIndicator
            contentContainerStyle={{ paddingHorizontal: space[6], paddingBottom: space[6] }}
          >
            {BETA_NOTICE_COPY.intro.map((paragraph, i) => (
              <Text key={paragraph} className={`text-body text-muted ${i === 0 ? "" : "mt-3"}`}>
                {paragraph}
              </Text>
            ))}

            <View className="mt-6 gap-3">
              {BETA_NOTICE_COPY.points.map((point) => (
                <View key={point} className="flex-row gap-3">
                  <View className="mt-2 h-1 w-1 rounded-pill bg-muted" />
                  <Text className="flex-1 text-body text-ink">{point}</Text>
                </View>
              ))}
            </View>

            <Text className="mt-6 text-caption text-muted">{BETA_NOTICE_COPY.finePrint}</Text>

            {/* The label is tappable too, so the whole row behaves like web's
                <label> rather than asking for a hit on the 22pt box. */}
            <View className="mt-6 flex-row items-center">
              <Checkbox
                checked={checked}
                onChange={setChecked}
                disabled={saving}
                accessibilityLabel={BETA_NOTICE_COPY.acknowledgement}
              />
              <Text
                className="ml-1 flex-1 text-body text-ink"
                onPress={saving ? undefined : () => setChecked(!checked)}
              >
                {BETA_NOTICE_COPY.acknowledgement}
              </Text>
            </View>
          </ScrollView>

          {/* Pinned footer — the CTA never scrolls away, and its top hairline
              doubles as the "content continues above" edge cue. */}
          <View
            className="border-t border-line px-6 pt-4"
            style={{ paddingBottom: Math.max(insets.bottom, space[8]) }}
          >
            <Button
              label={BETA_NOTICE_COPY.cta}
              onPress={handleAccept}
              disabled={!checked}
              loading={saving}
            />
          </View>
        </View>
      </View>
    </Modal>
  );
}
