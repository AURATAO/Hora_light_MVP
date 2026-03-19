import { Link } from 'react-router-dom'

export default function Terms() {
  return (
    <div className="min-h-screen px-4 py-12 bg-white text-slate-800">
      <div className="mx-auto max-w-2xl">
        <div className="mb-8">
          <Link to="/login" className="text-sm text-slate-500 hover:text-slate-700 underline">
            ← Back to login
          </Link>
        </div>

        <h1 className="text-2xl font-semibold mb-2">Terms of Use</h1>
        <p className="text-sm text-slate-500 mb-8">Last updated: March 2026</p>

        <div className="space-y-6 text-sm leading-relaxed text-slate-700">
          <section>
            <h2 className="font-semibold text-base mb-2">1. Acceptance of Terms</h2>
            <p>By accessing or using Hora Light, you agree to be bound by these Terms of Use. If you do not agree, please do not use the platform.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">2. Platform Use</h2>
            <p>Hora Light is a task marketplace connecting people who need help with tasks and people willing to assist. You may use the platform only for lawful purposes and in accordance with these Terms.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">3. User Responsibilities</h2>
            <ul className="list-disc pl-5 space-y-1">
              <li>You must be at least 18 years old to use this platform.</li>
              <li>You are responsible for the accuracy of information you provide.</li>
              <li>You agree not to post tasks that are illegal, harmful, or violate our community guidelines.</li>
              <li>You are responsible for your interactions with other users.</li>
            </ul>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">4. Companionship Tasks</h2>
            <p>Companionship tasks are limited to public-route accompaniment only. Any use of the companionship category for medical care, intimate services, or activities involving minors is strictly prohibited and may result in immediate account suspension.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">5. Payments</h2>
            <p>All payments are processed through the platform. Hora Light charges a service fee on completed tasks. Advance payments and time-based costs are calculated as described in the task creation flow.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">6. Disclaimers</h2>
            <p>Hora Light is provided "as is" during the beta period. We do not guarantee the quality, safety, or legality of tasks posted. Use the platform at your own discretion.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">7. Changes to Terms</h2>
            <p>We may update these Terms at any time. Continued use of the platform after changes constitutes acceptance of the new Terms.</p>
          </section>

          <section>
            <h2 className="font-semibold text-base mb-2">8. Contact</h2>
            <p>For questions about these Terms, contact us at <a href="mailto:support@horaapp.co" className="underline">support@horaapp.co</a>.</p>
          </section>
        </div>
      </div>
    </div>
  )
}
