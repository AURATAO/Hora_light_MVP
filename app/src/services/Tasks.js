import { supabase } from "../lib/supabaseClient";

export async function createTask({
  title,
  description,
  category = "task",
  is_immediate = true,
  scheduled_at = null,
}) {
  const {
    data: { user },
  } = await supabase.auth.getUser();
  if (!user) throw new Error("未登入");
  const { data, error } = await supabase
    .from("tasks")
    .insert([
      {
        requester: user.id,
        title,
        description,
        category,
        is_immediate,
        scheduled_at,
      },
    ])
    .select()
    .single();
  if (error) throw error;
  return data;
}

export async function listOpenTasks() {
  const { data, error } = await supabase
    .from("tasks")
    .select("id,title,location_text,created_at")
    .eq("status", "open")
    .order("created_at", { ascending: false });
  if (error) throw error;
  return data;
}

export async function acceptTask(taskId) {
  const {
    data: { user },
  } = await supabase.auth.getUser();
  if (!user) throw new Error("未登入");
  const { data, error } = await supabase
    .from("tasks")
    .update({ assigned_to: user.id })
    .eq("id", taskId)
    .is("assigned_to", null)
    .eq("status", "open")
    .select()
    .single();
  if (error) throw error;
  return data;
}
