import { Link } from 'react-router-dom'

export default function Privacy() {
  return (
    <div className="min-h-screen px-4 py-12 bg-white text-slate-800">
      <div className="mx-auto max-w-2xl">
        <div className="mb-8">
          <Link to="/login" className="text-sm text-slate-500 hover:text-slate-700 underline">
            ← Back to login
          </Link>
        </div>

        <h1 className="text-2xl font-semibold mb-2">Privacy Policy</h1>
        <p className="text-sm text-slate-500 mb-8">Last updated: March 2026</p>

        <div className="space-y-6 text-sm leading-relaxed text-slate-700">
          <section>
            <h2 className="font-semibold text-base mb-2">1. Information We Collect</h2>
            <ul className="list-disc pl-5 space-y-1">
              <li><strong>Account information:</strong> name, email address, profile photo, phone number, and city — provided when you sign up or edit your profile.</li>
              <li><strong>Task data:</strong> tasks you post or accept, locations, timestamps, and work logs.</li>
              <li><strong>Usage data:</strong> pages visited, actions taken, and device/browser information.</li>
            </ul>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">2. How We Use Your Information</h2>
            <ul className="list-disc pl-5 space-y-1">
              <li>To operate and improve the Hora Light platform.</li>
              <li>To facilitate connections between task posters and supporters.</li>
              <li>To process payments and maintain transaction records.</li>
              <li>To send platform-related notifications.</li>
            </ul>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">3. Information Sharing</h2>
            <p>We do not sell your personal data. We share information only:</p>
            <ul className="list-disc pl-5 space-y-1 mt-2">
              <li>With other users as needed to complete a task (e.g., your name and public profile).</li>
              <li>With service providers who help us operate the platform (e.g., authentication, hosting, chat).</li>
              <li>When required by law.</li>
            </ul>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">4. Data Retention</h2>
            <p>We retain your data for as long as your account is active. You may request deletion of your account and associated data by contacting us.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">5. Security</h2>
            <p>We use industry-standard security measures to protect your data. However, no method of transmission over the internet is 100% secure. Use the platform at your own risk.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">6. Third-Party Services</h2>
            <p>Hora Light uses third-party services including Google (authentication, maps) and Supabase (database). These services have their own privacy policies.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">7. Your Rights</h2>
            <p>You may request access to, correction of, or deletion of your personal data at any time by contacting us at <a href="mailto:support@horaapp.co" className="underline">support@horaapp.co</a>.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">8. Changes to This Policy</h2>
            <p>We may update this Privacy Policy from time to time. We will notify you of significant changes via the platform or email.</p>
          </section>
        </div>
      </div>
    </div>
  )
}
