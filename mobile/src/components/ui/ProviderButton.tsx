import type { ReactNode } from "react";
import { ActivityIndicator, Text, View } from "react-native";
import { PressableScale } from "./PressableScale";
import { cn } from "../../theme/cn";
import { color, opacity, radius, size, space, type } from "../../theme/tokens";

export interface ProviderButtonProps {
  label: string;
  logo: ReactNode;
  onPress?: () => void;
  disabled?: boolean;
  loading?: boolean;
  className?: string;
}

// The twin of the native Sign in with Apple button on the login screen: a
// black pill, logo + label centred as one group, sized from the
// `provider*` tokens so the two read as a matched pair (identical height,
// radius, type size and weight, logo size). Login-screen providers only —
// every other button is `Button`.
//
// Values go in `style`, not className, on purpose: the height and type size
// are the native button's, which exist nowhere in the Tailwind scale. The
// disabled dimming sits on an outer View because PressableScale's animated
// opacity would override it on the pressable itself.
export function ProviderButton({
  label,
  logo,
  onPress,
  disabled = false,
  loading = false,
  className,
}: ProviderButtonProps) {
  const inert = disabled || loading;

  return (
    <View className={cn(className)} style={{ opacity: disabled ? opacity.disabled : 1 }}>
      <PressableScale
        onPress={onPress}
        disabled={inert}
        accessibilityRole="button"
        accessibilityLabel={label}
        accessibilityState={{ disabled: inert, busy: loading }}
        className="flex-row items-center justify-center"
        style={{
          height: size.providerButtonHeight,
          borderRadius: radius.pill,
          backgroundColor: color.providerBlack,
          paddingHorizontal: space[4],
        }}
      >
        {loading ? (
          <ActivityIndicator color={color.white} />
        ) : (
          <>
            <View
              style={{
                width: size.providerLogo,
                height: size.providerLogo,
                marginRight: size.providerLogoGap,
                alignItems: "center",
                justifyContent: "center",
              }}
            >
              {logo}
            </View>
            <Text style={{ ...type.providerButton, color: color.white }}>{label}</Text>
          </>
        )}
      </PressableScale>
    </View>
  );
}
