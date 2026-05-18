import { Link, useNavigate } from 'react-router-dom'
import { Zap, ShoppingBasket, WashingMachine, Heart, Clock, Sparkles, Users } from 'lucide-react'

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

  return (
    <div className="min-h-screen bg-primary text-accent py-10 px-4">
      <div className="max-w-2xl mx-auto space-y-8">

        <div>
          <h1 className="font-heading text-3xl text-white">What do you need?</h1>
          <p className="text-white/40 mt-1 text-sm font-secondary">
            Choose a category to get started.
          </p>
        </div>

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
