// TODO(you): fill in the real HO:RA Concierge WhatsApp number, digits only
// (E.164 without the leading "+"), e.g. "19083242201" per the web app's
// app/src/components/WhatsAppFloat.jsx. Left as an obviously-fake
// placeholder so an unfilled number fails loudly instead of silently
// linking users to the wrong contact.
export const HORA_WHATSAPP_NUMBER = "19083242201";

// Support inbox on the live my-hora.com domain. Web's SupporterStatusBanner
// still points at the outdated support@horaapp.co — see decisions/D-08.
export const SUPPORT_EMAIL = "info@my-hora.com";

export const LEGAL_URLS = {
  terms: "https://www.my-hora.com/terms",
  privacy: "https://www.my-hora.com/privacy",
} as const;
