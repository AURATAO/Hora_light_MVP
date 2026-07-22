import { useCallback, useEffect, useState } from "react";
import { Text, View } from "react-native";
import { useRouter } from "expo-router";
import { CircleCheck, ChevronLeft } from "lucide-react-native";
import { Button, Input, PressableScale, Screen, Skeleton } from "../components/ui";
import { ApiError, applySupporter, getProfile, updateProfile } from "../lib/api";
import { color, size } from "../theme/tokens";

interface FieldErrors {
  firstName?: string;
  lastName?: string;
  phone?: string;
  city?: string;
}

function splitName(name: string | null | undefined): { first: string; last: string } {
  const parts = (name ?? "").trim().split(/\s+/).filter(Boolean);
  return { first: parts[0] ?? "", last: parts.slice(1).join(" ") };
}

// Full supporter application, matching web's BecomeSupporter field-for-field.
// Replaces the old name-only sheet: the same PATCH /profile → POST
// /supporter/apply → GET /profile sequence web runs, so both clients leave the
// same rows behind and both read the status back from the server (S-05).
export default function SupporterApply() {
  const router = useRouter();

  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [firstName, setFirstName] = useState("");
  const [lastName, setLastName] = useState("");
  const [phone, setPhone] = useState("");
  const [city, setCity] = useState("");
  const [email, setEmail] = useState("");

  const [errors, setErrors] = useState<FieldErrors>({});
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [submitted, setSubmitted] = useState(false);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const load = useCallback(async () => {
    try {
      const p = await getProfile();
      const { first, last } = splitName(p.name);
      setFirstName(first);
      setLastName(last);
      setPhone(p.phone ?? "");
      setCity(p.city ?? "");
      setEmail(p.email ?? "");
      setLoadError(null);
    } catch (e) {
      if (handleAuthError(e)) return;
      setLoadError(e instanceof Error ? e.message : "Couldn't load your details");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Prefill once on mount — not on every focus, which would discard whatever
  // the applicant has typed if they leave and come back.
  useEffect(() => {
    load();
  }, [load]);

  async function handleSubmit() {
    const next: FieldErrors = {};
    if (!firstName.trim()) next.firstName = "First name is required";
    if (!lastName.trim()) next.lastName = "Last name is required";
    if (!phone.trim()) next.phone = "Phone is required";
    if (!city.trim()) next.city = "City is required";
    setErrors(next);
    if (Object.keys(next).length > 0) return;

    setSubmitting(true);
    setSubmitError(null);
    try {
      await updateProfile({
        name: `${firstName.trim()} ${lastName.trim()}`,
        phone: phone.trim(),
        city: city.trim(),
      });
      await applySupporter({ first_name: firstName.trim(), last_name: lastName.trim() });
      // /supporter/apply answers { ok: true } only — re-read the profile so the
      // Work tab we return to renders the server's status, not a guessed one.
      await getProfile();
      setSubmitted(true);
      setSubmitting(false);
    } catch (e) {
      if (handleAuthError(e)) return;
      setSubmitError(e instanceof Error ? e.message : "Couldn't submit your application. Try again.");
      setSubmitting(false);
    }
  }

  if (submitted) {
    return (
      <Screen>
        <View className="mt-12 gap-3 rounded-card border border-line bg-surface p-4">
          <CircleCheck color={color.brand} size={24} strokeWidth={size.iconStroke} />
          <View className="gap-1">
            <Text className="text-title font-semibold text-ink">Application submitted</Text>
            <Text className="text-body text-muted">
              We&apos;ll review it and send you a background check link within 1–2 business days.
            </Text>
          </View>
          <Button label="Back to work" onPress={() => router.back()} />
        </View>
      </Screen>
    );
  }

  return (
    <Screen>
      <View className="mb-6 mt-4 flex-row items-center">
        <PressableScale
          onPress={() => router.back()}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
        <Text className="ml-1 text-title font-semibold text-ink">Apply to become a supporter</Text>
      </View>

      <Text className="mb-4 text-body text-muted">
        We&apos;ll review your application and send you a background check link within 1–2 business days.
      </Text>

      <View className="mb-6 gap-1 rounded-card bg-gold-tint p-4">
        <Text className="text-body font-semibold text-gold-text">Current eligibility requirements</Text>
        <Text className="text-body text-gold-text">• Must be located in the United States</Text>
        <Text className="text-body text-gold-text">• Must have a valid US government-issued ID</Text>
      </View>

      {loading ? (
        <View className="gap-3">
          <Skeleton className="h-[52px]" />
          <Skeleton className="h-[52px]" />
          <Skeleton className="h-[52px]" />
          <Skeleton className="h-[52px]" />
        </View>
      ) : (
        <>
          <View className="gap-3">
            <Input
              label="First name"
              value={firstName}
              onChangeText={setFirstName}
              placeholder="Jane"
              error={errors.firstName}
              autoCapitalize="words"
            />
            <Input
              label="Last name"
              value={lastName}
              onChangeText={setLastName}
              placeholder="Doe"
              error={errors.lastName}
              autoCapitalize="words"
            />
            <Input
              label="Phone"
              value={phone}
              onChangeText={setPhone}
              placeholder="(555) 555-5555"
              keyboardType="phone-pad"
              error={errors.phone}
            />
            <Input
              label="City"
              value={city}
              onChangeText={setCity}
              placeholder="New York, NY"
              error={errors.city}
            />
            <View>
              <Text className="mb-1 text-caption text-muted">Email</Text>
              <View className="h-[52px] justify-center rounded-sm border border-line bg-page px-4">
                <Text className="text-body text-muted">{email || "—"}</Text>
              </View>
            </View>
          </View>

          {loadError ? <Text className="mt-3 text-caption text-danger">{loadError}</Text> : null}
          {submitError ? <Text className="mt-3 text-caption text-danger">{submitError}</Text> : null}

          <View className="mb-4 mt-6">
            <Button label="Submit application" onPress={handleSubmit} loading={submitting} />
          </View>
        </>
      )}
    </Screen>
  );
}
