import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Zap, ShoppingBasket, WashingMachine, Heart, Clock, Sparkles, Users } from 'lucide-react'
import { api } from '../api/client'

const ICON_COLOR = '#9aab3a'
const ICON_SIZE = 28

const CATEGORIES = [
  {
    key: 'delivery',
    Icon: Zap,
    title: 'Same-day Delivery',
    subtitle: 'Packages, pickups, drop-offs',
  },
  {
    key: 'grocery',
    Icon: ShoppingBasket,
    title: 'Grocery & Errands',
    subtitle: 'Shopping, pharmacy, supplies',
  },
  {
    key: 'laundry',
    Icon: WashingMachine,
    title: 'Laundry Service',
    subtitle: 'Wash, fold, dry cleaning',
  },
  {
    key: 'companionship',
    Icon: Users,
    title: 'Companionship',
    subtitle: 'Appointments, walks, company',
  },
  {
    key: 'queue',
    Icon: Clock,
    title: 'Queue & Wait',
    subtitle: 'Lines, reservations, waiting',
  },
  {
    key: 'anything_else',
    Icon: Sparkles,
    title: 'Anything Else',
    subtitle: 'Whatever you need',
  },
]

export default function CategoryHome() {
  const nav = useNavigate()

  const [aiInput, setAiInput] = useState('')
  const [aiLoading, setAiLoading] = useState(false)
  const [aiError, setAiError] = useState('')
  const [categoryOpen, setCategoryOpen] = useState(false)

  async function handleAiSubmit() {
    if (!aiInput.trim() || aiLoading) return
    setAiLoading(true)
    setAiError('')
    try {
      const parsed = await api('/ai/parse-task', { method: 'POST', body: { input: aiInput } })
      nav('/tasks/new', { state: { aiPrefill: parsed } })
    } catch (err) {
      setAiError("Couldn't understand that — try choosing a category below.")
      setCategoryOpen(true)
    } finally {
      setAiLoading(false)
    }
  }

  return (
    <div className="min-h-screen bg-primary text-accent py-10 px-4">
      <div className="max-w-2xl mx-auto space-y-8">

        {/* AI Input Section */}
        <div className="mb-8">
          <h1 className="text-3xl font-heading text-white mb-2">
            What do you need help with?
          </h1>
          <p className="text-white/50 text-sm mb-4">
            Describe your task and we'll set it up for you.
          </p>

          {/* Input + Button */}
          <div className="relative">
            <textarea
              rows={4}
              className="w-full rounded-xl border border-white/15 bg-white/5
                         px-4 py-3 pr-16 text-base text-white placeholder-white/30
                         outline-none focus:border-secondary/50 resize-none"
              placeholder='e.g. Pick up dry cleaning from 34th St, drop at my office on Lex Ave'
              value={aiInput}
              onChange={e => setAiInput(e.target.value)}
              onKeyDown={e => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  handleAiSubmit()
                }
              }}
            />
            <button
              onClick={handleAiSubmit}
              disabled={!aiInput.trim() || aiLoading}
              className="absolute bottom-3 right-3 rounded-lg px-3 py-2
                         text-sm font-semibold text-white
                         disabled:opacity-40 transition-all"
              style={{ backgroundColor: '#9aab3a' }}
            >
              {aiLoading ? '...' : '→'}
            </button>
          </div>

          {aiError && (
            <p className="text-xs text-red-400 mt-2">{aiError}</p>
          )}
        </div>

        {/* Browse by category - collapsible */}
        <div>
          <button
            onClick={() => setCategoryOpen(prev => !prev)}
            className="flex items-center gap-2 text-sm text-white/40
                       hover:text-white/60 transition-colors"
          >
            <span>{categoryOpen ? '▲' : '▼'}</span>
            <span>Browse by category</span>
          </button>
        </div>

        {/* Category grid - only show when open */}
        {categoryOpen && (
          <div className="grid grid-cols-2 gap-4">
            {CATEGORIES.map(({ key, Icon, title, subtitle }) => (
              <button
                key={key}
                type="button"
                onClick={() => nav(`/tasks/new?category=${key}`)}
                className="rounded-2xl border border-secondary/20 bg-surface p-5 flex flex-col gap-3
                           hover:border-secondary/50 hover:bg-white/5 transition-colors group text-left"
              >
                <Icon size={ICON_SIZE} color={ICON_COLOR} strokeWidth={1.75} />
                <div>
                  <div className="font-secondary font-semibold text-white group-hover:text-secondary transition-colors">
                    {title}
                  </div>
                  <div className="text-xs text-white/40 mt-0.5">{subtitle}</div>
                </div>
              </button>
            ))}
          </div>
        )}

        <div className="text-center pt-2">
          <Link
            to="/my"
            className="text-sm text-white/40 hover:text-white/70 transition-colors underline-offset-2 hover:underline"
          >
            View My Tasks →
          </Link>
        </div>

      </div>
    </div>
  )
}
