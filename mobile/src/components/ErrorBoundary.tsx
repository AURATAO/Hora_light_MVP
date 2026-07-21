import { Component, type ErrorInfo, type ReactNode } from "react";
import { Text } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { TriangleAlert } from "lucide-react-native";
import { Button } from "./ui/Button";
import { color, size } from "../theme/tokens";

interface Props {
  children: ReactNode;
}

interface State {
  hasError: boolean;
}

// Root crash guard. A render or lifecycle error anywhere below this boundary
// would otherwise tear the whole tree down to a blank white screen. Instead we
// catch it and show a friendly full-screen fallback whose single Restart clears
// the boundary and remounts the children — the boot flow (auth check, routing)
// runs again from scratch, which recovers most transient failures.
//
// User-facing copy is plain language with no stack trace or internal id (S-33);
// the full error still goes to the JS console for development.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false };

  static getDerivedStateFromError(): State {
    return { hasError: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Root ErrorBoundary caught:", error, info.componentStack);
  }

  private reset = () => this.setState({ hasError: false });

  render() {
    if (!this.state.hasError) return this.props.children;

    return (
      <SafeAreaView className="flex-1 items-center justify-center gap-4 bg-page px-6">
        <TriangleAlert color={color.danger} size={32} strokeWidth={size.iconStroke} />
        <Text className="text-title font-semibold text-ink">Something went wrong</Text>
        <Text className="text-center text-body text-muted">
          The app ran into an unexpected error. Restart to get back on track.
        </Text>
        <Button label="Restart" onPress={this.reset} />
      </SafeAreaView>
    );
  }
}
