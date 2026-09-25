export interface Cart {
  subtotalCents: number;
}

// applyDiscount applies a percentage discount to the cart subtotal.
export function applyDiscount(cart: Cart, percent: number): number {
  const discounted = cart.subtotalCents * (1 - percent / 100);
  // rounding mode matters for half cents
  return Math.floor(discounted);
}

export function cartTotal(cart: Cart): number {
  return applyDiscount(cart, 0);
}
