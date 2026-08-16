import { useEffect, useRef, useState } from "react";
import { Text, View } from "react-native";
import { MapPin } from "lucide-react-native";
import { Input, PressableScale } from "./ui";
import {
  fetchPlaceDetails,
  fetchSuggestions,
  newSessionToken,
  placesConfigured,
  withApt,
  type PlaceSuggestion,
} from "../lib/places";
import { color, size } from "../theme/tokens";

// Google is billed per keystroke-batch, so don't start querying on one or two
// characters — web can afford it on desktop typing, a phone can't.
const MIN_QUERY_LENGTH = 3;
const DEBOUNCE_MS = 250;

export interface AddressFieldProps {
  value: string;
  onChangeText: (text: string) => void;
  label?: string;
  placeholder?: string;
  error?: string;
}

/**
 * Mobile equivalent of web's AddressInput + PlaceInput pair
 * (app/src/components/AddressInput.jsx, PlaceInput.jsx): a text field that
 * suggests NYC-biased places as you type, plus an Apt / Suite / Floor line that
 * appears once a place is picked and is spliced into the label the same way web
 * splices it — so `location_text` stays format-compatible across platforms.
 *
 * Selection is never required: whatever the user types is emitted as-is, which
 * keeps manual free-text entry (and the no-API-key case) working unchanged.
 */
export function AddressField({ value, onChangeText, label, placeholder, error }: AddressFieldProps) {
  const [query, setQuery] = useState(value);
  const [apt, setApt] = useState("");
  const [selected, setSelected] = useState(false);
  const [suggestions, setSuggestions] = useState<PlaceSuggestion[]>([]);
  const [open, setOpen] = useState(false);
  const [resolving, setResolving] = useState(false);

  // The composed string we last handed to the parent. Anything else arriving in
  // `value` came from outside (AI parse, form reset) and must reset our local
  // place/apt state — the label is no longer one we composed.
  const lastEmitted = useRef(value);
  const sessionRef = useRef(newSessionToken());
  // Set right after a selection so the debounce effect doesn't immediately
  // re-query the label it just wrote and re-open the list under the user.
  const skipNextQuery = useRef(false);

  useEffect(() => {
    if (value === lastEmitted.current) return;
    lastEmitted.current = value;
    skipNextQuery.current = true;
    setQuery(value);
    setApt("");
    setSelected(false);
    setSuggestions([]);
    setOpen(false);
  }, [value]);

  function emit(nextQuery: string, nextApt: string, isSelected: boolean) {
    const composed = isSelected ? withApt(nextQuery, nextApt) : nextQuery;
    lastEmitted.current = composed;
    onChangeText(composed);
  }

  function handleChangeText(text: string) {
    setQuery(text);
    // Typing over a selected place makes it free text again (web does the same:
    // PlaceInput emits `{ label: v }` with no placeId on manual input).
    setSelected(false);
    setApt("");
    emit(text, "", false);
  }

  function handleAptChange(text: string) {
    setApt(text);
    emit(query, text, selected);
  }

  useEffect(() => {
    if (!placesConfigured()) return;
    if (skipNextQuery.current) {
      skipNextQuery.current = false;
      return;
    }
    const text = query.trim();
    if (text.length < MIN_QUERY_LENGTH) {
      setSuggestions([]);
      setOpen(false);
      return;
    }

    const controller = new AbortController();
    const timer = setTimeout(async () => {
      try {
        const results = await fetchSuggestions(text, sessionRef.current, controller.signal);
        if (controller.signal.aborted) return;
        setSuggestions(results);
        setOpen(results.length > 0);
      } catch {
        // Suggestions are an accelerator, not a requirement — on any failure the
        // field silently stays a plain text input.
        if (controller.signal.aborted) return;
        setSuggestions([]);
        setOpen(false);
      }
    }, DEBOUNCE_MS);

    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [query]);

  async function handleSelect(suggestion: PlaceSuggestion) {
    setOpen(false);
    setSuggestions([]);
    setResolving(true);
    skipNextQuery.current = true;
    setQuery(suggestion.text);
    setSelected(true);
    emit(suggestion.text, apt, true);

    try {
      const details = await fetchPlaceDetails(suggestion.placeId, suggestion.text, sessionRef.current);
      skipNextQuery.current = true;
      setQuery(details.label);
      emit(details.label, apt, true);
    } catch {
      // Keep the prediction text — it is already a usable address string.
    } finally {
      // A session ends with its Details call, successful or not.
      sessionRef.current = newSessionToken();
      setResolving(false);
    }
  }

  return (
    <View className="gap-2">
      {/* A resolved Places label ("350 5th Ave, Apt 12B, New York, NY 10118")
          is longer than one line of this field far more often than not, so it
          grows rather than hiding most of the address off the right edge. */}
      <Input
        label={label}
        grow
        value={query}
        onChangeText={handleChangeText}
        onFocus={() => setOpen(suggestions.length > 0)}
        placeholder={placeholder ?? "Street address"}
        error={error}
        autoCorrect={false}
        autoCapitalize="words"
      />

      {open ? (
        <View className="overflow-hidden rounded-sm border border-line bg-surface">
          {suggestions.map((suggestion, i) => (
            <PressableScale
              key={suggestion.placeId}
              onPress={() => handleSelect(suggestion)}
              accessibilityRole="button"
              className={`min-h-11 flex-row items-center gap-2 px-4 py-3 ${i > 0 ? "border-t border-line" : ""}`}
            >
              <MapPin color={color.muted} size={16} strokeWidth={size.iconStroke} />
              <View className="flex-1">
                <Text className="text-body text-ink" numberOfLines={1}>
                  {suggestion.mainText}
                </Text>
                {suggestion.secondaryText ? (
                  <Text className="text-caption text-muted" numberOfLines={1}>
                    {suggestion.secondaryText}
                  </Text>
                ) : null}
              </View>
            </PressableScale>
          ))}
        </View>
      ) : null}

      {resolving ? <Text className="text-caption text-muted">Getting the full address…</Text> : null}

      {selected ? (
        <Input
          value={apt}
          onChangeText={handleAptChange}
          placeholder="Apt / Suite / Floor (optional)"
          autoCapitalize="words"
        />
      ) : null}
    </View>
  );
}
