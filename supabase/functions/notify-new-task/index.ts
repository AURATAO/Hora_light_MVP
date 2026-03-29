import { serve } from "https://deno.land/std@0.168.0/http/server.ts";

const LOGO_URL = "https://horaapp.co/Logo.svg";
const APP_URL = "https://horaapp.co";
const IG_URL = "https://www.instagram.com/my_hora_app/";

function buildHtml(record: Record<string, string>): string {
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>New Task on HO:RA</title>
</head>
<body style="margin:0;padding:0;background:#f4f4f5;font-family:sans-serif;">
  <table width="100%" cellpadding="0" cellspacing="0" style="background:#f4f4f5;padding:32px 0;">
    <tr>
      <td align="center">
        <table width="560" cellpadding="0" cellspacing="0" style="background:#222831;border-radius:16px;overflow:hidden;max-width:560px;width:100%;">

          <!-- Logo header -->
          <tr>
            <td align="center" style="padding:32px 32px 20px;">
              <a href="${APP_URL}" style="text-decoration:none;">
                <img src="${LOGO_URL}" alt="HO:RA" height="32" style="display:block;" />
              </a>
            </td>
          </tr>

          <!-- Divider -->
          <tr><td style="padding:0 32px;"><hr style="border:none;border-top:1px solid rgba(255,255,255,0.1);margin:0;" /></td></tr>

          <!-- Title -->
          <tr>
            <td style="padding:28px 32px 8px;">
              <p style="margin:0;font-size:11px;letter-spacing:1px;text-transform:uppercase;color:#A3C585;font-weight:600;">New Task Posted</p>
              <h1 style="margin:8px 0 0;font-size:22px;font-weight:700;color:#E5E7EB;">${record.title ?? "—"}</h1>
            </td>
          </tr>

          <!-- Details -->
          <tr>
            <td style="padding:20px 32px;">
              <table width="100%" cellpadding="0" cellspacing="0">
                ${row("Category", record.category)}
                ${row("Description", record.description)}
                ${row("Location", record.location_text)}
                ${row("Estimated time", record.estimated_minutes ? `${record.estimated_minutes} min` : "—")}
                ${row("Posted by", record.requester)}
                ${row("Task ID", record.id)}
              </table>
            </td>
          </tr>

          <!-- CTA -->
          <tr>
            <td align="center" style="padding:8px 32px 32px;">
              <a href="${APP_URL}" style="display:inline-block;background:#A3C585;color:#222831;font-weight:700;font-size:14px;text-decoration:none;padding:12px 32px;border-radius:999px;">
                View on HO:RA
              </a>
            </td>
          </tr>

          <!-- Divider -->
          <tr><td style="padding:0 32px;"><hr style="border:none;border-top:1px solid rgba(255,255,255,0.1);margin:0;" /></td></tr>

          <!-- Footer -->
          <tr>
            <td align="center" style="padding:20px 32px 28px;">
              <p style="margin:0 0 8px;font-size:12px;color:rgba(229,231,235,0.4);">Follow us for updates</p>
              <a href="${IG_URL}" style="display:inline-block;text-decoration:none;">
                <img src="https://upload.wikimedia.org/wikipedia/commons/thumb/a/a5/Instagram_icon.png/240px-Instagram_icon.png"
                  alt="Instagram" width="24" height="24"
                  style="display:block;border-radius:6px;opacity:0.7;" />
              </a>
              <p style="margin:16px 0 0;font-size:11px;color:rgba(229,231,235,0.25);">
                © ${new Date().getFullYear()} HO:RA · <a href="${APP_URL}" style="color:rgba(163,197,133,0.5);text-decoration:none;">horaapp.co</a>
              </p>
            </td>
          </tr>

        </table>
      </td>
    </tr>
  </table>
</body>
</html>`;
}

function row(label: string, value: string | undefined): string {
  return `<tr>
    <td style="padding:6px 0;font-size:12px;color:rgba(229,231,235,0.45);width:36%;vertical-align:top;">${label}</td>
    <td style="padding:6px 0;font-size:13px;color:#E5E7EB;vertical-align:top;">${value ?? "—"}</td>
  </tr>`;
}

serve(async (req) => {
  let payload: Record<string, unknown>;
  try {
    payload = await req.json();
  } catch {
    return new Response("invalid json", { status: 400 });
  }

  const record = payload.record as Record<string, string> | undefined;
  if (!record) {
    return new Response("missing record", { status: 400 });
  }

  const body = {
    From: "Ho:ra <no-reply@horaapp.co>",
    To: "liang.you@arcodiax.com, daniele@arcodiax.com",
    Subject: `New task: ${record.title ?? "Untitled"}`,
    HtmlBody: buildHtml(record),
    TextBody: [
      `New task posted on HO:RA`,
      `Title: ${record.title}`,
      `Category: ${record.category}`,
      `Description: ${record.description}`,
      `Location: ${record.location_text}`,
      `Estimated time: ${record.estimated_minutes} mins`,
      `Posted by: ${record.requester}`,
      `Task ID: ${record.id}`,
      ``,
      `Follow us on Instagram: ${IG_URL}`,
    ].join("\n"),
    MessageStream: "outbound",
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
