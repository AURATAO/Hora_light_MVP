import { Modal, Pressable, Image, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { X } from "lucide-react-native";
import { PressableScale } from "./ui";
import { color, size } from "../theme/tokens";

export interface PhotoViewerProps {
  /** The photo to show; null hides the viewer. */
  uri: string | null;
  /** What it is, for VoiceOver ("Receipt photo"). */
  label: string;
  onClose: () => void;
}

/**
 * One photo, full-screen — the receipt behind a reimbursement, the
 * proof-of-work behind a completion.
 *
 * Deliberately minimal: the whole backdrop closes it, there is no zoom or
 * gallery, and the image keeps its own aspect ratio. The thumbnails on the
 * task screen are cropped; this is where the requester actually reads the
 * receipt. Same two-layer shape as every sheet: the backdrop is a
 * self-closing Pressable SIBLING of the content, never a wrapper, so the
 * Close control stays its own focus stop under VoiceOver.
 */
export function PhotoViewer({ uri, label, onClose }: PhotoViewerProps) {
  const insets = useSafeAreaInsets();
  return (
    <Modal visible={!!uri} transparent animationType="fade" onRequestClose={onClose}>
      <View className="flex-1 bg-ink">
        <Pressable className="absolute inset-0" onPress={onClose} accessibilityLabel="Close" />
        {uri ? (
          // pointerEvents on the wrapper: the image must not swallow the
          // backdrop's tap, and Image itself does not take the prop.
          <View className="flex-1" pointerEvents="none">
            <Image
              source={{ uri }}
              className="flex-1"
              resizeMode="contain"
              accessibilityLabel={label}
              accessibilityIgnoresInvertColors
            />
          </View>
        ) : null}
        <View
          className="absolute left-0 right-0 flex-row items-center justify-between px-6"
          style={{ top: insets.top + 8 }}
        >
          <Text className="text-body font-semibold text-white">{label}</Text>
          <PressableScale
            onPress={onClose}
            hitSlop={8}
            className="h-11 w-11 items-center justify-center rounded-pill bg-surface"
            accessibilityRole="button"
            accessibilityLabel="Close"
          >
            <X color={color.ink} size={20} strokeWidth={size.iconStroke} />
          </PressableScale>
        </View>
      </View>
    </Modal>
  );
}
