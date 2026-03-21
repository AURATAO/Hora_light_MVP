import { serve } from "https://deno.land/std@0.168.0/http/server.ts";

serve(async (req) => {
  const payload = await req.json();
  const record = payload.record;

  const body = {
    From: "Ho:ra <no-reply@horaapp.co>",
    To: "liang.you@horaapp.co",
    Subject: "New task posted on HO:RA",
    TextBody: [
      `Task ID: ${record.id}`,
      `Title: ${record.title}`,
      `Category: ${record.category}`,
      `Description: ${record.description}`,
      `Location: ${record.location_text}`,
      `Estimated time: ${record.estimated_minutes} mins`,
      `Posted by: ${record.requester}`,
      `Created at: ${record.created_at}`,
    ].join("\n"),
  };

  const res = await fetch("https://api.postmarkapp.com/email", {
    method: "POST",
    headers: {
      "X-Postmark-Server-Token": Deno.env.get("SMTP_PASS") ?? "",
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });

  if (!res.ok) {
    const err = await res.text();
    console.error("[notify-new-task] Postmark error:", err);
    return new Response(err, { status: 500 });
  }

  return new Response("ok", { status: 200 });
});
