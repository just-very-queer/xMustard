// fetchJson loads a JSON document from the API.
export async function fetchJson(url: string): Promise<unknown> {
  const res = await fetch(url);
  return res.json();
}
