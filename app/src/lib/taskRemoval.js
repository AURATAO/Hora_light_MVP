// Why a task was taken down, in the requester's words rather than the slug.
// The backend sends the same explanation by push and email; this is what the
// task page shows when they open it afterwards. The strings are mobile's
// (mobile/src/lib/task-utils.ts REMOVAL_NOTICE), pinned by taskRemoval.test.mjs.
const REMOVAL_NOTICE = {
  out_of_scope_private_residence:
    "This task was removed because it falls outside HO:RA's scope (public locations only — no private residences). Feel free to post it again at a public location!",
  out_of_scope_other:
    "This task was removed because it falls outside HO:RA's scope (short, in-person tasks at public locations). Feel free to post it again within scope!",
  inappropriate:
    "This task was removed because it doesn't meet our community guidelines. Get in touch if you think this was a mistake.",
  other:
    "This task was removed by the HO:RA team. Get in touch if you think this was a mistake — you're welcome to post again.",
}

export function removalNotice(reason) {
  return (reason && REMOVAL_NOTICE[reason]) || REMOVAL_NOTICE.other
}

/** A task the viewer can no longer read because it was taken down: getTask
 *  answers 403 `task_removed` to a supporter who was detached from it. */
export function isTaskRemovedError(err) {
  return err?.status === 403 && err?.body?.error === 'task_removed'
}

export const REMOVED_GONE_TITLE = 'This task has been removed'
export const REMOVED_GONE_BODY =
  "The HO:RA team took this task down. More tasks are coming — have a look at what's available."
