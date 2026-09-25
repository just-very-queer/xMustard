// formatCurrency renders an amount in cents as a currency string.
export function formatCurrency(cents: number, currency = "USD"): string {
  return (cents / 100).toFixed(2) + " " + currency;
}

// formatPercent renders a ratio as a percentage.
export function formatPercent(ratio: number): string {
  return (ratio * 100).toFixed(1) + "%";
}
