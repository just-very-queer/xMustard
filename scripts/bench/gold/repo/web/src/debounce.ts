// debounce delays calls to fn until wait milliseconds pass without another call.
export function debounce<T extends unknown[]>(fn: (...args: T) => void, wait: number) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return (...args: T) => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => fn(...args), wait);
  };
}
