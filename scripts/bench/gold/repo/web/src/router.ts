export interface Route {
  path: string;
  name: string;
}

// matchRoute returns the first route whose path pattern matches the URL path.
export function matchRoute(routes: Route[], urlPath: string): Route | undefined {
  return routes.find((r) => r.path === urlPath || urlPath.startsWith(r.path + "/"));
}
