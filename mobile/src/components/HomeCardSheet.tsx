import { useEffect, useState } from "react";
import { Image, Modal, Pressable, ScrollView, Text, View, useWindowDimensions } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { ImageIcon } from "lucide-react-native";
import { Button } from "./ui";
import { HOME_CARD_DETAILS, HOME_CARD_IMAGES, parseEmphasis } from "../lib/home-content";
import { color, size, space } from "../theme/tokens";

export interface HomeCardSheetProps {
  /** Slug of the open card, or null when the sheet is closed. */
  slug: string | null;
  onDismiss: () => void;
  /** "Apply now" and friends: the sheet closes first, then this routes. */
  onPrimary: () => void;
}

// Same geometry as CompanionshipPolicySheet — the sheet is capped, and the
// scrollable body takes what the illustration, header and pinned footer leave.
const SHEET_MAX_HEIGHT_RATIO = 0.85;
const BODY_MAX_HEIGHT_RATIO = 0.7;
const ILLUSTRATION_HEIGHT = 140;

// Explainer sheet behind the "Get to know HO:RA" cards. All copy comes from
// HOME_CARD_DETAILS; this only lays it out.
export function HomeCardSheet({ slug, onDismiss, onPrimary }: HomeCardSheetProps) {
  const { height } = useWindowDimensions();
  const insets = useSafeAreaInsets();

  // Retain the last opened slug so the slide-out animation still has content
  // to render after the parent clears `slug`.
  const [rendered, setRendered] = useState<string | null>(slug);
  useEffect(() => {
    if (slug) setRendered(slug);
  }, [slug]);

  const detail = rendered ? HOME_CARD_DETAILS[rendered] : undefined;
  const image = rendered ? HOME_CARD_IMAGES[rendered] : undefined;

  return (
    <Modal visible={!!slug && !!detail} transparent animationType="slide" onRequestClose={onDismiss}>
      <View className="flex-1 justify-end">
        {/* Backdrop is its own layer rather than a Pressable wrapping the
            sheet: a ScrollView nested inside a pressable parent loses the
            responder negotiation on drag, which leaves the body unscrollable
            (and its last paragraph unreachable). */}
        <Pressable className="absolute inset-0 bg-ink/40" onPress={onDismiss} />

        <View
          className="overflow-hidden rounded-t-card bg-surface"
          style={{ maxHeight: height * SHEET_MAX_HEIGHT_RATIO }}
        >
          {/* Same illustration slot as the card itself, same brandTint
              placeholder when the slug has no art wired (see NewsCard). */}
          {image ? (
            <Image
              source={image}
              resizeMode="cover"
              style={{ width: "100%", height: ILLUSTRATION_HEIGHT }}
            />
          ) : (
            <View
              className="items-center justify-center bg-brand-tint"
              style={{ height: ILLUSTRATION_HEIGHT }}
            >
              <ImageIcon color={color.brand} size={24} strokeWidth={size.iconStroke} />
            </View>
          )}

          <View className="px-6 pb-3 pt-6">
            <Text className="text-title font-semibold text-ink">{detail?.title}</Text>
          </View>

          <ScrollView
            // flexShrink matters as much as maxHeight: illustration + header +
            // body + footer can exceed the sheet's own cap, and RN children
            // don't shrink by default — without it the footer gets pushed off
            // the bottom of the clamped sheet on shorter screens.
            style={{ maxHeight: height * BODY_MAX_HEIGHT_RATIO, flexShrink: 1 }}
            showsVerticalScrollIndicator
            contentContainerStyle={{
              paddingHorizontal: space[6],
              paddingBottom: space[6],
              gap: space[3],
            }}
          >
            {detail?.body.map((paragraph, i) => (
              <Text key={i} className="text-body text-ink">
                {parseEmphasis(paragraph).map((span, j) => (
                  <Text key={j} className={span.strong ? "font-semibold" : undefined}>
                    {span.text}
                  </Text>
                ))}
              </Text>
            ))}
          </ScrollView>

          {/* Pinned footer — always visible, never scrolls away. Its top
              hairline doubles as the "content continues above" edge cue. */}
          <View
            className="gap-2 border-t border-line px-6 pt-4"
            style={{ paddingBottom: Math.max(insets.bottom, space[8]) }}
          >
            <Button
              label={detail?.primaryLabel ?? ""}
              onPress={detail?.primaryAction ? onPrimary : onDismiss}
            />
            {detail?.dismissLabel ? (
              <Button label={detail.dismissLabel} variant="text" onPress={onDismiss} />
            ) : null}
          </View>
        </View>
      </View>
    </Modal>
  );
}
