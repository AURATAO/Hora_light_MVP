import { useState, type ReactNode } from "react";
import { View, Text } from "react-native";
import {
  Screen,
  Button,
  Pill,
  Card,
  TaskCard,
  Badge,
  Input,
  Skeleton,
  type ButtonVariant,
} from "../components/ui";
import { color } from "../theme/tokens";

// Dev-only design-system showcase. Not linked from the tab bar — reachable via
// /dev-components (long-press the Profile headline, or type the URL directly).
// TODO: remove before TestFlight.

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <View className="mb-8">
      <Text className="mb-3 text-caption font-semibold text-muted">{title}</Text>
      {children}
    </View>
  );
}

const BUTTON_VARIANTS: ButtonVariant[] = ["primary", "secondary", "text"];

function ButtonShowcase() {
  return (
    <View className="gap-4">
      {BUTTON_VARIANTS.map((variant) => (
        <View key={variant} className="gap-2">
          <Text className="text-caption text-muted">{variant}</Text>
          <View className="flex-row flex-wrap gap-3">
            <Button label="Continue" variant={variant} onPress={() => {}} />
            <Button label="Disabled" variant={variant} disabled onPress={() => {}} />
            <Button label="Loading" variant={variant} loading onPress={() => {}} />
          </View>
        </View>
      ))}
      <Text className="text-caption text-muted">
        Press and hold any button above to see the press feedback (scale 0.97, spring back).
      </Text>
    </View>
  );
}

const CATEGORIES = ["Cleaning", "Moving", "Assembly", "Errands", "Yard work"];

function PillShowcase() {
  const [selected, setSelected] = useState(CATEGORIES[0]);

  return (
    <View className="gap-3">
      <View className="flex-row flex-wrap gap-2">
        <Pill label="Selected" selected onPress={() => {}} />
        <Pill label="Unselected" onPress={() => {}} />
      </View>
      <Text className="text-caption text-muted">Category row (tap to select)</Text>
      <View className="flex-row flex-wrap gap-2">
        {CATEGORIES.map((c) => (
          <Pill key={c} label={c} selected={selected === c} onPress={() => setSelected(c)} />
        ))}
      </View>
    </View>
  );
}

function CardShowcase() {
  return (
    <View className="gap-3">
      <Card>
        <Text className="text-body text-ink">
          Plain card — surface bg, hairline border, radius card, padding 16, no shadow.
        </Text>
      </Card>

      <TaskCard
        title="Assemble bookshelf"
        price="$45"
        metadata="2.1 km away · Posted 3 hours ago"
      />
      <TaskCard
        title="Move a couch"
        price="$80"
        metadata="Assigned to Alex M. · Starts tomorrow, 10:00"
        // Gold badge here is illustrative only (showcase completeness) — DESIGN.md
        // §1 reserves gold for tips / Human Project in real screens, never status.
        badges={<Badge label="Assigned" variant="gold" />}
      />
      <TaskCard
        title="Yard cleanup"
        price="$60"
        metadata="Completed · 5-star review"
        badges={<Badge label="Completed" variant="success" />}
      />
    </View>
  );
}

function InputShowcase() {
  return (
    <View className="gap-4">
      <Input label="Empty" placeholder="you@example.com" />
      <Input label="Filled" defaultValue="alex@example.com" />
      <Input label="Focus (tap to see)" placeholder="Border turns ink on focus" />
      <Input label="Error" defaultValue="not-an-email" error="Enter a valid email address." />
    </View>
  );
}

function SkeletonShowcase() {
  return (
    <View className="gap-4">
      <View className="gap-2">
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="h-4 w-3/4" />
        <Skeleton className="h-4 w-full" />
      </View>
      <View>
        <Text className="mb-2 text-caption text-muted">Task card skeleton</Text>
        <Card>
          <View className="flex-row items-center justify-between">
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-6 w-12" />
          </View>
          <Skeleton className="mt-2 h-3 w-2/3" />
        </Card>
      </View>
    </View>
  );
}

function TypographyShowcase() {
  return (
    <View className="gap-2">
      <Text className="text-display font-semibold text-ink">Display — screen headline</Text>
      <Text className="text-title font-semibold text-ink">Title — section headers, prices</Text>
      <Text className="text-body font-normal text-ink">Body — default text, inputs</Text>
      <Text className="text-body font-semibold text-ink">Body strong — buttons, emphasis</Text>
      <Text className="text-caption font-normal text-muted">Caption — metadata, timestamps</Text>
      <Text className="text-micro font-semibold text-ink">Micro — tab labels, badges</Text>
    </View>
  );
}

const SWATCHES: { key: keyof typeof color; className: string }[] = [
  { key: "ink", className: "bg-ink" },
  { key: "brand", className: "bg-brand" },
  { key: "brandTint", className: "bg-brand-tint" },
  { key: "gold", className: "bg-gold" },
  { key: "goldTint", className: "bg-gold-tint" },
  { key: "goldText", className: "bg-gold-text" },
  { key: "muted", className: "bg-muted" },
  { key: "line", className: "bg-line" },
  { key: "inactive", className: "bg-inactive" },
  { key: "page", className: "bg-page" },
  { key: "surface", className: "bg-surface" },
  { key: "danger", className: "bg-danger" },
  { key: "transparent", className: "bg-transparent" },
  { key: "white", className: "bg-white" },
];

function ColorShowcase() {
  return (
    <View className="flex-row flex-wrap gap-4">
      {SWATCHES.map(({ key, className }) => (
        <View key={key} className="items-center gap-1">
          <View className={`h-12 w-12 rounded-sm border border-line ${className}`} />
          <Text className="text-micro font-semibold text-ink">{key}</Text>
          <Text className="text-micro text-muted">{color[key]}</Text>
        </View>
      ))}
    </View>
  );
}

export default function DevComponents() {
  return (
    <Screen headline="Components">
      <Section title="Button">
        <ButtonShowcase />
      </Section>
      <Section title="Pill">
        <PillShowcase />
      </Section>
      <Section title="Card">
        <CardShowcase />
      </Section>
      <Section title="Badge">
        <View className="flex-row gap-2">
          <Badge label="Success" variant="success" />
          <Badge label="Gold" variant="gold" />
        </View>
      </Section>
      <Section title="Input">
        <InputShowcase />
      </Section>
      <Section title="Skeleton">
        <SkeletonShowcase />
      </Section>
      <Section title="Typography">
        <TypographyShowcase />
      </Section>
      <Section title="Color tokens">
        <ColorShowcase />
      </Section>
    </Screen>
  );
}
