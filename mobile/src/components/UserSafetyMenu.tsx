import { useState } from "react";
import { Alert, type AlertButton } from "react-native";
import { MoreHorizontal } from "lucide-react-native";
import { PressableScale } from "./ui";
import { ReportUserSheet } from "./ReportUserSheet";
import { blockUser } from "../lib/api";
import { BLOCK_CONFIRM_BODY, blockConfirmTitle, safetyTargetName } from "../lib/safety";
import { color, size } from "../theme/tokens";

// The "⋯" button that carries Report and Block (App Store Guideline 1.2), for
// the chat header and the task detail header — both roles, wherever the app
// shows the other party on a task. The server checks that `userId` really is
// the other party on `taskId`; this component only decides what to draw.
// `userId` may be the viewer (the sandbox's self-accepted task): the menu and
// the Report sheet still open, and the server's "You can't report or block
// yourself." is what the sheet and the block alert then show.

export interface UserSafetyMenuProps {
  userId: string;
  userName?: string | null;
  taskId: string;
  /** Already blocked (task.chat_blocked): Block is not offered twice. */
  blocked?: boolean;
  /** Refetch after a block, so the screen goes read-only straight away. */
  onBlocked?: () => void;
}

export function UserSafetyMenu({ userId, userName, taskId, blocked, onBlocked }: UserSafetyMenuProps) {
  const [reportOpen, setReportOpen] = useState(false);
  const name = safetyTargetName(userName);

  function confirmBlock() {
    Alert.alert(blockConfirmTitle(userName), BLOCK_CONFIRM_BODY, [
      { text: "Cancel", style: "cancel" },
      {
        text: "Block",
        style: "destructive",
        onPress: async () => {
          try {
            const res = await blockUser(userId, taskId);
            Alert.alert("Blocked", res.message);
            onBlocked?.();
          } catch (e) {
            Alert.alert("Couldn't block", e instanceof Error ? e.message : "Try again in a moment.");
          }
        },
      },
    ]);
  }

  function openMenu() {
    const actions: AlertButton[] = [
      { text: `Report ${name}`, onPress: () => setReportOpen(true) },
    ];
    if (!blocked) {
      actions.push({ text: `Block ${name}`, style: "destructive", onPress: confirmBlock });
    }
    actions.push({ text: "Cancel", style: "cancel" });
    Alert.alert(name.charAt(0).toUpperCase() + name.slice(1), "Report a problem or block this person.", actions);
  }

  return (
    <>
      <PressableScale
        onPress={openMenu}
        hitSlop={8}
        className="h-11 w-11 items-center justify-center rounded-pill"
        accessibilityRole="button"
        accessibilityLabel={`Report or block ${name}`}
      >
        <MoreHorizontal color={color.ink} size={22} strokeWidth={size.iconStroke} />
      </PressableScale>
      <ReportUserSheet
        visible={reportOpen}
        userId={userId}
        userName={userName}
        taskId={taskId}
        onClose={() => setReportOpen(false)}
      />
    </>
  );
}
