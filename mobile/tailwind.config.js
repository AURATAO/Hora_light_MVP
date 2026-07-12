const colors = require("../design-tokens/colors");

/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./src/**/*.{js,jsx,ts,tsx}", "./App.tsx"],
  presets: [require("nativewind/preset")],
  theme: {
    colors,
    spacing: {
      0: "0px",
      1: "4px",
      2: "8px",
      3: "12px",
      4: "16px",
      6: "24px",
      8: "32px",
      12: "48px",
    },
    borderRadius: {
      none: "0px",
      sm: "8px",
      card: "16px",
      pill: "999px",
    },
    fontSize: {
      display: ["26px", { lineHeight: "32px" }],
      title: ["18px", { lineHeight: "24px" }],
      body: ["15px", { lineHeight: "22px" }],
      caption: ["13px", { lineHeight: "18px" }],
      micro: ["11px", { lineHeight: "14px" }],
    },
    fontWeight: {
      normal: "400",
      semibold: "600",
    },
    extend: {},
  },
  plugins: [],
};
