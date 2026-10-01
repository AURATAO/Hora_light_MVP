/** Where "Contact support" goes. The live domain, the same address mobile
 *  uses (mobile/src/lib/constants.ts SUPPORT_EMAIL, decision D-08). Web said
 *  support@horaapp.co long after the product stopped using that domain. */
export const SUPPORT_EMAIL = 'info@my-hora.com'

export function supportMailto(subject) {
  return `mailto:${SUPPORT_EMAIL}${subject ? `?subject=${encodeURIComponent(subject)}` : ''}`
}
