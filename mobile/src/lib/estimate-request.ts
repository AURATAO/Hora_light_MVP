import type { EstimateTaskCostPayload } from "./api";
import { zeroSeconds } from "./task-utils";
import type { TaskCategory } from "./types";

/** The form fields a quote is priced from. A structural slice of TaskFormState,
 *  so this module stays free of the component (and testable under Node). */
export interface EstimateInputs {
  category: TaskCategory | undefined;
  estimatedMinutes: string;
  shoppingBudget: string;
  isImmediate: boolean;
  scheduledDate: Date;
}

/**
 * The body of POST /tasks/estimate for the form as it stands, or null when
 * there is nothing to quote yet (no category, or no positive duration).
 *
 * THE RE-QUOTE RULE LIVES HERE. The form re-fetches whenever this request
 * changes — it keys its effect on the serialized result — so anything the
 * server prices from is, by construction, something that triggers a new quote.
 * The effect used to list its dependencies by hand and left the schedule out:
 * moving a task from 6 PM to 10 PM kept the $0.50/min card on screen for a
 * task that was then held and billed at $1.00. A field added to this payload
 * can no longer be forgotten that way.
 */
export function estimateRequest(
  form: EstimateInputs,
  promoCode?: string
): EstimateTaskCostPayload | null {
  const minutes = form.estimatedMinutes ? Number(form.estimatedMinutes) : NaN;
  if (!form.category || !Number.isFinite(minutes) || minutes <= 0) return null;
  const budget = form.shoppingBudget ? Number(form.shoppingBudget) : 0;
  return {
    category: form.category,
    estimated_minutes: minutes,
    prepay_amount_cents: Number.isFinite(budget) && budget > 0 ? Math.round(budget * 100) : 0,
    // The rate depends on when the work happens, not on when this form was
    // opened: a 21:30 task filled in at 6pm is quoted the evening rate.
    is_immediate: form.isImmediate,
    scheduled_at: form.isImmediate ? "" : zeroSeconds(form.scheduledDate).toISOString(),
    // Quoted the way the post will apply it; a refused code comes back as a
    // sentence beside a still-correct quote.
    promo_code: promoCode || undefined,
  };
}
