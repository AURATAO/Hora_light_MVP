import Svg, { Path } from "react-native-svg";
import { color } from "../../theme/tokens";

export interface GoogleLogoProps {
  size: number;
}

// The Google "G" mark in its official four colours (tokens, not literals, so
// DESIGN.md §8 check 1 holds). Inlined for the same reason as Logo.tsx: no
// svg transformer, no asset that can go missing at runtime.
export function GoogleLogo({ size }: GoogleLogoProps) {
  return (
    <Svg width={size} height={size} viewBox="0 0 48 48">
      <Path
        fill={color.googleYellow}
        d="M43.6 20.5H42V20H24v8h11.3C33.7 32 29.3 35 24 35c-7.2 0-13-5.8-13-13S16.8 9 24 9c3.3 0 6.3 1.2 8.6 3.2l5.7-5.7C34.6 3.4 29.6 1.5 24 1.5 11.5 1.5 1.5 11.5 1.5 24S11.5 46.5 24 46.5c12 0 22-9 22-22 0-1.5-.2-3-.4-4z"
      />
      <Path
        fill={color.googleRed}
        d="M6.3 14.6l6.6 4.9C14.5 15.9 18.9 13 24 13c3.3 0 6.3 1.2 8.6 3.2l5.7-5.7C34.6 7.4 29.6 5.5 24 5.5c-7.8 0-14.4 4.3-17.7 10.6z"
      />
      <Path
        fill={color.googleGreen}
        d="M24 42.5c5.1 0 9.7-1.7 13.3-4.7l-6.1-5.1C29.1 34 26.7 35 24 35c-5.2 0-9.6-3.5-11.2-8.3l-6.6 5.1C9.4 38.1 16.1 42.5 24 42.5z"
      />
      <Path
        fill={color.googleBlue}
        d="M46 24c0-1.5-.2-3-.4-4H24v8h11.3c-.8 3.7-3.2 6.2-6 7.7l6.1 5.1C38.3 38.7 46 32.5 46 24z"
      />
    </Svg>
  );
}
