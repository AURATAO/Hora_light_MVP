import { View } from "react-native";
import { Pill } from "./ui";
import { NYC_BOROUGHS } from "../lib/api";

// The five boroughs as pills, any number selected (D-24). A supporter's areas
// decide who hears about a new task FIRST; everyone else eligible hears three
// minutes later, so choosing none is not "no offers", only "no head start".
export function BoroughPicker({
  selected,
  onChange,
  options = NYC_BOROUGHS,
  disabled = false,
}: {
  selected: string[];
  onChange: (next: string[]) => void;
  options?: readonly string[];
  disabled?: boolean;
}) {
  function toggle(name: string) {
    onChange(selected.includes(name) ? selected.filter((b) => b !== name) : [...selected, name]);
  }
  return (
    <View className="flex-row flex-wrap gap-2">
      {options.map((name) => (
        <Pill
          key={name}
          label={name === "the Bronx" ? "The Bronx" : name}
          selected={selected.includes(name)}
          onPress={() => toggle(name)}
          disabled={disabled}
        />
      ))}
    </View>
  );
}
