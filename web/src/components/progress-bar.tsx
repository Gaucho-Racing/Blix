interface ProgressBarProps {
  value: number
  max?: number
  label: string
  valueText?: string
}

export function ProgressBar({
  value,
  max = 100,
  label,
  valueText,
}: ProgressBarProps) {
  const maximum = Number.isFinite(max) && max > 0 ? max : 1
  const current = Number.isFinite(value)
    ? Math.min(maximum, Math.max(0, value))
    : 0

  return (
    <div
      className="progress-bar"
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={maximum}
      aria-valuenow={current}
      aria-valuetext={valueText}
    >
      <span
        className="progress-bar-fill"
        aria-hidden="true"
        style={{ width: `${(current / maximum) * 100}%` }}
      />
    </div>
  )
}
